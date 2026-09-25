"""Official SDK contracts; run only against the Go-owned disposable fixture."""
import json
import os
from urllib.parse import urlparse
from urllib.request import urlopen

from openai import OpenAI, NotFoundError
from openapi_spec_validator import validate

url = os.environ["GATEMUX_SDK_FIXTURE_URL"]
assert urlparse(url).hostname in {"127.0.0.1", "localhost", "::1"}, "local fixture only"
client = OpenAI(api_key=os.environ["GATEMUX_SDK_FIXTURE_KEY"], base_url=url + "/v1", max_retries=0, timeout=10)
alias = os.environ["GATEMUX_SDK_FIXTURE_ALIAS"]

models = client.models.list()
assert alias in [model.id for model in models]
chat = client.chat.completions.create(model=alias, messages=[{"role":"user","content":"hello"}], extra_body={"seed":123})
assert chat.model == alias and chat.choices[0].message.content == "ok"
chunks = list(client.chat.completions.create(model=alias, messages=[{"role":"user","content":"hello"}], stream=True))
assert chunks[-1].model == alias and chunks[-1].choices[0].delta.content == "ok"
embedding = client.embeddings.create(model=alias, input=[[1,2],[3]], encoding_format="float")
assert embedding.model == alias and embedding.data[0].embedding == [0.1,0.2,0.3]

response = client.responses.create(model=alias, input="hello", reasoning={"effort":"low"}, tools=[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}])
assert response.id.startswith("resp_gatemux_") and response.model == alias and response.output_text == "hello"
assert response.usage.input_tokens_details.cached_tokens == 4
assert response.usage.output_tokens_details.reasoning_tokens == 1
assert client.responses.retrieve(response.id).id == response.id
followup = client.responses.create(model=alias, input="follow up", previous_response_id=response.id)
assert followup.previous_response_id == response.id
events = list(client.responses.create(model=alias, input="stream", stream=True))
assert events[0].type == "response.created" and events[-1].type == "response.completed"
assert events[-1].response.model == alias and events[-1].response.output_text == "hello"
assert not client.responses.input_items.list(response.id).has_more
client.responses.delete(response.id)
try:
    client.responses.retrieve(response.id)
    raise AssertionError("deleted response is readable")
except NotFoundError:
    pass
stateless = client.responses.create(model=alias, input="stateless", store=False)
try:
    client.responses.retrieve(stateless.id)
    raise AssertionError("store=false response was persisted")
except NotFoundError:
    pass

with urlopen(url + "/openapi/v1.json", timeout=10) as source:
    spec = json.load(source)
validate(spec)
assert "/v1/responses" in spec["paths"]
assert "text/event-stream" in spec["paths"]["/v1/responses"]["post"]["responses"]["200"]["content"]
print("PASS: official openai SDK chat, token embeddings, models, Responses create/stream/multi-turn/retrieve/input-items/delete/store=false; OpenAPI 3.1 validation")
