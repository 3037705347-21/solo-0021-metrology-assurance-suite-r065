package contracts

import "example.com/solo-0021-metrology-assurance-suite/internal/domain"

type RegisterDeviceRequest struct {
	AssetTag string `json:"asset_tag"`
	Model    string `json:"model"`
	Room     string `json:"room"`
	Steward  string `json:"steward"`
}

type OpenCaseRequest struct {
	DeviceID  string  `json:"device_id"`
	Nominal   float64 `json:"nominal"`
	Tolerance float64 `json:"tolerance"`
	Steward   string  `json:"steward"`
}

type RecordMeasurementRequest struct {
	Observed   float64 `json:"observed"`
	Technician string  `json:"technician"`
	ClaimID    string  `json:"claim_id,omitempty"`
}

type ReopenCaseRequest struct {
	Actor   string `json:"actor"`
	Reason  string `json:"reason"`
	ClaimID string `json:"claim_id,omitempty"`
}

type SealCaseRequest struct {
	Reviewer string `json:"reviewer"`
	Summary  string `json:"summary"`
	ClaimID  string `json:"claim_id,omitempty"`
}

type ClaimCaseRequest struct {
	Holder     string `json:"holder"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

type ReleaseCaseRequest struct {
	ClaimID string `json:"claim_id"`
}

type DeviceResponse struct {
	Device domain.Device `json:"device"`
}

type CaseResponse struct {
	Case         domain.VerificationCase `json:"case"`
	Measurements []domain.Measurement    `json:"measurements"`
	Seal         *domain.EvidenceSeal    `json:"seal,omitempty"`
	Claim        *domain.CaseClaim       `json:"claim,omitempty"`
	Events       []domain.AuditEvent     `json:"events"`
}

type ClaimResponse struct {
	Claim domain.CaseClaim `json:"claim"`
}

type ReleaseResponse struct {
	Released bool `json:"released"`
}

type EventsResponse struct {
	Events []domain.AuditEvent `json:"events"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
