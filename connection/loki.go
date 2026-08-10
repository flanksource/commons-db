package connection

// Loki is an HTTP endpoint like any other, and the connection form stores its
// credentials under the shared HTTP authentication contract (Properties keyed
// by authType). Embedding HTTPConnection is what makes basic, OAuth, bearer and
// mTLS all reachable, the same way PrometheusConnection does it.
//
// +kubebuilder:object:generate=true
type Loki struct {
	HTTPConnection `json:",inline" yaml:",inline"`
}

func (c *Loki) Populate(ctx ConnectionContext) error {
	// An inline URL is the caller's own endpoint. A stored connection that names
	// one overrides it, but hydrating against a row without a URL must not blank
	// it — that is the only endpoint such a caller has.
	inlineURL := c.URL

	if _, err := c.Hydrate(ctx, ctx.GetNamespace()); err != nil {
		return err
	}

	if c.URL == "" {
		c.URL = inlineURL
	}
	return nil
}
