package store

import "time"

type Team struct {
	ID                   int64
	Slug                 string
	Name                 string
	UsdLimitCents        *int64
	Period               string
	AllowedModels        []string
	RPM                  *int
	TPM                  *int
	MaxParallelRequests  *int
	CapturePayloads      bool
	CustomerRegistration string
	CreatedAt            time.Time
	ArchivedAt           *time.Time
}

func (t *Team) AllowsModel(alias string) bool {
	if t == nil {
		return true
	}
	return allowsModel(t.AllowedModels, alias)
}

type VirtualKey struct {
	ID                       int64
	TeamID                   int64
	UserID                   *int64
	ServiceAccountID         *int64
	KeyPrefix                string
	Name                     string
	Metadata                 map[string]any
	AllowedModels            []string
	AllowedCIDRs             []string
	ScopedRPM                *int
	ScopedTPM                *int
	MaxParallelRequests      *int
	ScopedUsdLimitCents      *int64
	ExpiresAt                *time.Time
	RotatedFromKeyID         *int64
	LastUsedAt               *time.Time
	CreatedAt                time.Time
	RevokedAt                *time.Time
	DisabledAt               *time.Time
	OwnerUserEmail           string
	OwnerUserName            string
	ServiceAccountName       string
	OwnerMaxParallelRequests *int
}

func (vk *VirtualKey) AllowsModel(alias string) bool {
	if vk == nil {
		return true
	}
	return allowsModel(vk.AllowedModels, alias)
}
