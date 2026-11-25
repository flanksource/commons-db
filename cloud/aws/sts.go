package aws

import "context"

// STS is the Security Token Service client (query protocol).
type STS struct{ svc *service }

// STS returns the STS client. STS is global in the commons sense: the same
// region-scoped endpoint serves it.
func (s *Session) STS() *STS { return &STS{svc: s.newService("sts", "sts", "sts")} }

// CallerIdentity is the principal the credentials resolve to.
type CallerIdentity struct {
	Account string `xml:"Account"`
	ARN     string `xml:"Arn"`
	UserID  string `xml:"UserId"`
}

type getCallerIdentityResponse struct {
	Result CallerIdentity `xml:"GetCallerIdentityResult"`
}

// GetCallerIdentity returns the account, ARN and user id of the signing principal.
func (c *STS) GetCallerIdentity(ctx context.Context) (*CallerIdentity, error) {
	var out getCallerIdentityResponse
	if err := c.svc.queryCall(ctx, "2011-06-15", "GetCallerIdentity", nil, &out); err != nil {
		return nil, err
	}
	return &out.Result, nil
}
