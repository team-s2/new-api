package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"google.golang.org/protobuf/encoding/protowire"
)

// DevinModelInfo is one entry of the account-scoped model catalog, filtered
// down to what the gateway cares about.
type DevinModelInfo struct {
	Label          string   `json:"label,omitempty"`
	ModelUID       string   `json:"model_uid,omitempty"`
	Disabled       bool     `json:"disabled,omitempty"`
	IsPremium      bool     `json:"is_premium,omitempty"`
	IsRecommended  bool     `json:"is_recommended,omitempty"`
	SupportsImages bool     `json:"supports_images,omitempty"`
	MaxTokens      int64    `json:"max_tokens,omitempty"`
	AllowedTiers   []uint64 `json:"allowed_tiers,omitempty"`
	PricingType    uint64   `json:"pricing_type,omitempty"`
	PromoStatus    uint64   `json:"promo_status,omitempty"`
	ModelCostTier  uint64   `json:"model_cost_tier,omitempty"`
	IsNew          bool     `json:"is_new,omitempty"`
}

// DevinUsageProbe is the sanitized GetUserStatus + GetCliModelConfigs result
// surfaced by the channel usage endpoint.
type DevinUsageProbe struct {
	PlanName            string           `json:"plan_name,omitempty"`
	TeamsTier           uint64           `json:"teams_tier,omitempty"`
	IsDevin             bool             `json:"is_devin,omitempty"`
	Name                string           `json:"name,omitempty"`
	Email               string           `json:"email,omitempty"`
	UserID              string           `json:"user_id,omitempty"`
	OrgID               string           `json:"org_id,omitempty"`
	AccountDisplayName  string           `json:"account_display_name,omitempty"`
	IsPro               bool             `json:"pro,omitempty"`
	SubagentDefaultUID  string           `json:"subagent_default_model_uid,omitempty"`
	Models              []DevinModelInfo `json:"models,omitempty"`
	TotalCatalogEntries int              `json:"total_catalog_entries,omitempty"`
}

func devinBaseURLForService(baseURL string) string {
	base := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://server.codeium.com"
	}
	return base
}

// FetchDevinUsage probes the seat/plan status of a Devin subscription
// credential and the SWE-2 model entries of the account catalog.
func FetchDevinUsage(ctx context.Context, cred *DevinCredential, baseURL, proxyURL string) (*DevinUsageProbe, error) {
	if cred == nil || cred.AccessToken() == "" {
		return nil, errors.New("devin subscription channel: missing credential")
	}
	token := cred.AccessToken()
	base := devinBaseURLForService(baseURL)
	probe := &DevinUsageProbe{}

	statusPayload, err := postDevinConnectRPC(ctx,
		base+devinSeatServicePath+"/GetUserStatus", token, proxyURL,
		buildDevinMetadataRequest(token))
	if err != nil {
		return nil, err
	}
	decodeDevinUserStatus(statusPayload, probe)

	if modelsPayload, err := postDevinConnectRPC(ctx,
		base+devinAPIServicePath+"/GetCliModelConfigs", token, proxyURL,
		buildDevinMetadataRequest(token)); err == nil {
		decodeDevinModelCatalog(modelsPayload, probe)
	}
	return probe, nil
}

// decodeDevinUserStatus parses ExaSeatManagementPb_GetUserStatusResponse:
// { user_status: 1, plan_info: 2 }. Unverified credit and permission fields
// are deliberately omitted until their schema and units are confirmed.
func decodeDevinUserStatus(payload []byte, probe *DevinUsageProbe) {
	fields := devinDecodeWireFields(payload)

	if planInfoFields := devinFirstSubfields(fields, 2); planInfoFields != nil {
		probe.TeamsTier = devinFirstVarint(planInfoFields, 1)
		probe.PlanName = devinFirstString(planInfoFields, 2)
		probe.IsDevin = devinFirstVarint(planInfoFields, 34) != 0
		if devinInfo := devinFirstSubfields(planInfoFields, 33); devinInfo != nil {
			if probe.OrgID == "" {
				probe.OrgID = devinFirstString(devinInfo, 4)
			}
			probe.AccountDisplayName = devinFirstString(devinInfo, 8)
		}
	}

	userStatus := devinFirstSubfields(fields, 1)
	if userStatus == nil {
		return
	}
	probe.IsPro = devinFirstVarint(userStatus, 1) != 0
	probe.Name = devinFirstString(userStatus, 3)
	probe.Email = devinFirstString(userStatus, 7)
	probe.UserID = devinFirstString(userStatus, 36)
}

// decodeDevinModelCatalog parses GetCliModelConfigsResponse and keeps only the
// two SWE-2 families this channel serves.
func decodeDevinModelCatalog(payload []byte, probe *DevinUsageProbe) {
	fields := devinDecodeWireFields(payload)
	if uid := strings.ToLower(strings.TrimSpace(devinFirstString(fields, 4))); uid != "" {
		if resolved, ok := common.ResolveDevinModelUID(uid); ok && resolved == uid {
			probe.SubagentDefaultUID = uid
		}
	}

	configs := fields[1]
	probe.TotalCatalogEntries = len(configs)
	for _, cfgBytes := range configs {
		cfg := devinDecodeWireFields(cfgBytes)
		modelUID := strings.ToLower(strings.TrimSpace(devinFirstString(cfg, 22)))
		resolved, supported := common.ResolveDevinModelUID(modelUID)
		if !supported || resolved != modelUID {
			continue
		}
		info := DevinModelInfo{
			Label:          devinFirstString(cfg, 1),
			ModelUID:       modelUID,
			Disabled:       devinFirstVarint(cfg, 4) != 0,
			IsPremium:      devinFirstVarint(cfg, 7) != 0,
			IsRecommended:  devinFirstVarint(cfg, 11) != 0,
			SupportsImages: devinFirstVarint(cfg, 5) != 0,
			MaxTokens:      devinFirstInt64(cfg, 18),
			PricingType:    devinFirstVarint(cfg, 13),
			PromoStatus:    devinFirstVarint(cfg, 19),
			ModelCostTier:  devinFirstVarint(cfg, 24),
			IsNew:          devinFirstVarint(cfg, 15) != 0,
		}
		info.AllowedTiers = devinPackedVarints(cfg[12])
		probe.Models = append(probe.Models, info)
	}
}

// FetchDevinChannelModels queries the account model catalog and returns the
// SWE-2 model_uids the channel can serve, plus the bare family aliases when
// the corresponding high-effort model is enabled in the catalog.
func FetchDevinChannelModels(channel *model.Channel) ([]string, error) {
	key, _, apiErr := channel.GetNextEnabledKey()
	if apiErr != nil {
		return nil, apiErr
	}
	cred, err := ParseDevinCredential(strings.TrimSpace(key))
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	probe, err := FetchDevinUsage(ctx, cred, channel.GetBaseURL(), channel.GetSetting().Proxy)
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var models []string
	hasSWE2, hasFlash := false, false
	for _, m := range probe.Models {
		uid := strings.ToLower(strings.TrimSpace(m.ModelUID))
		resolved, supported := common.ResolveDevinModelUID(uid)
		if !supported || resolved != uid || m.Disabled || seen[uid] {
			continue
		}
		seen[uid] = true
		models = append(models, uid)
		if uid == "swe-2-flash-high" {
			hasFlash = true
		} else if uid == "swe-2-high" {
			hasSWE2 = true
		}
	}
	if len(models) == 0 {
		return nil, errors.New("devin subscription channel: catalog returned no swe-2 family models")
	}
	slices.Sort(models)
	if hasFlash {
		models = append([]string{"swe-2-flash"}, models...)
	}
	if hasSWE2 {
		models = append([]string{"swe-2"}, models...)
	}
	return models, nil
}

func devinFirstSubfields(fields map[int][][]byte, num int) map[int][][]byte {
	vals := fields[num]
	if len(vals) == 0 {
		return nil
	}
	return devinDecodeWireFields(vals[0])
}

func devinFirstVarint(fields map[int][][]byte, num int) uint64 {
	vals := fields[num]
	if len(vals) == 0 {
		return 0
	}
	return devinConsumeVarintValue(vals[0])
}

func devinFirstInt64(fields map[int][][]byte, num int) int64 {
	return int64(devinFirstVarint(fields, num))
}

func devinFirstString(fields map[int][][]byte, num int) string {
	vals := fields[num]
	if len(vals) == 0 {
		return ""
	}
	return string(vals[0])
}

// devinPackedVarints decodes a packed repeated varint field (e.g.
// allowed_tiers) from its raw bytes value.
func devinPackedVarints(values [][]byte) []uint64 {
	var out []uint64
	for _, raw := range values {
		pos := 0
		for pos < len(raw) {
			v, n := protowire.ConsumeVarint(raw[pos:])
			if n <= 0 {
				break
			}
			pos += n
			out = append(out, v)
		}
	}
	return out
}
