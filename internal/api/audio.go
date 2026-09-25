package api

import (
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
)

// AudioTranscriptions and AudioTranslations are multipart upload
// passthroughs to the upstream provider's Whisper-compatible endpoint.
// We route on the `model` field in the multipart form, then re-encode
// a fresh multipart body with the upstream model substituted (so an
// alias can route to a deployment whose upstream_model differs from
// the alias name without breaking the upload boundary).
func (h *V1Handler) AudioTranscriptions(w http.ResponseWriter, r *http.Request) {
	h.audioMultipart(w, r, capAudioTranscribe, "/v1/audio/transcriptions")
}

func (h *V1Handler) AudioTranslations(w http.ResponseWriter, r *http.Request) {
	h.audioMultipart(w, r, capAudioTranscribe, "/v1/audio/translations")
}

const (
	capAudioTranscribe passthroughCapability = "audio_transcribe"
	capAudioSpeech     passthroughCapability = "audio_speech"
)

// AudioSpeech is a JSON-in, binary-out endpoint (TTS). We route on the
// JSON body's `model` field, forward, and stream the audio bytes back.
func (h *V1Handler) AudioSpeech(w http.ResponseWriter, r *http.Request) {
	h.passthrough(w, r, capAudioSpeech, "/v1/audio/speech")
}

// File content above 1 MiB spills to disk. Re-encoding streams through a pipe
// with one bounded producer; payloads are never duplicated in a bytes.Buffer.
func (h *V1Handler) audioMultipart(w http.ResponseWriter, r *http.Request, cap passthroughCapability, upstreamPath string) {
	started := time.Now()
	team := auth.TeamFromContext(r.Context())
	if team == nil {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "no team in context")
		return
	}

	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not parse multipart upload: "+err.Error())
		return
	}
	defer r.MultipartForm.RemoveAll()
	for _, files := range r.MultipartForm.File {
		for _, file := range files {
			if file.Size > 25<<20 {
				writeJSONError(w, http.StatusRequestEntityTooLarge, "invalid_request", "audio file exceeds 25 MiB limit")
				return
			}
		}
	}

	model := r.FormValue("model")
	if model == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "model field is required")
		return
	}

	if !h.modelAllowed(team, auth.VirtualKeyFromContext(r.Context()), model) {
		writeJSONError(w, http.StatusForbidden, "model_not_allowed", "model not allowed for this api key: "+model)
		return
	}
	releaseModel, admitted := h.admitRequestConcurrency(w, r, model, r.PostFormValue("user"))
	if !admitted {
		return
	}
	defer releaseModel()
	if !h.admitUnpricedCustomer(w, r, model) {
		return
	}
	resolved, permit, err := h.resolveForCapability(r.Context(), model, cap)
	if err != nil {
		writeCapabilityRoutingError(w, err)
		return
	}
	defer h.releaseUpstreamPermit(permit)
	upstreamURL, apiKey, err := upstreamFor(resolved, upstreamPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	body, contentType := streamMultipart(r.Context(), r.MultipartForm, resolved.UpstreamModel)
	defer body.Close() // closes the pipe and joins producer before RemoveAll
	upReq, err := http.NewRequestWithContext(r.Context(), "POST", upstreamURL, body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	upReq.Header.Set("Content-Type", contentType)
	if apiKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	status, err := h.forwardProxy(w, r, upReq, h.streamingFor(resolved.DeploymentName), false)
	h.recordNativeAudit(r, resolved, model, started, status)
	abortIncompleteProxy(err)
}

type multipartBody struct {
	*io.PipeReader
	done chan struct{}
}

func (b *multipartBody) Close() error {
	err := b.PipeReader.Close()
	<-b.done
	return err
}

func streamMultipart(ctx context.Context, form *multipart.Form, model string) (*multipartBody, string) {
	reader, writer := io.Pipe()
	mw := multipart.NewWriter(writer)
	body := &multipartBody{reader, make(chan struct{})}
	go func() {
		defer close(body.done)
		err := encodeMultipart(ctx, mw, form, model)
		if err == nil {
			err = mw.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	return body, mw.FormDataContentType()
}

func encodeMultipart(ctx context.Context, mw *multipart.Writer, form *multipart.Form, model string) error {
	for name, values := range form.Value {
		for _, value := range values {
			if err := ctx.Err(); err != nil {
				return err
			}
			if name == "model" {
				value = model
			}
			if err := mw.WriteField(name, value); err != nil {
				return err
			}
		}
	}
	for name, files := range form.File {
		for _, file := range files {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := func() error {
				f, err := file.Open()
				if err != nil {
					return err
				}
				defer f.Close()
				headers := make(textproto.MIMEHeader)
				disposition := mime.FormatMediaType("form-data", map[string]string{"name": name, "filename": file.Filename})
				if disposition == "" {
					return fmt.Errorf("invalid multipart filename")
				}
				headers.Set("Content-Disposition", disposition)
				headers.Set("Content-Type", file.Header.Get("Content-Type"))
				part, err := mw.CreatePart(headers)
				if err != nil {
					return err
				}
				_, err = io.Copy(part, f)
				return err
			}(); err != nil {
				return err
			}
		}
	}
	return nil
}
