package query

type SampleResolution struct {
	PagingModes    string    `json:"pagingModes"`
	NativePaging   bool      `json:"nativePaging"`
	Pageable       bool      `json:"pageable"`
	PageableReason string    `json:"pageableReason,omitempty"`
	Order          Order     `json:"order"`
	Limits         RowLimits `json:"limits"`
}

func resolveSampleResolution(profile Profile) (SampleResolution, error) {
	order, err := profile.EffectiveOrder()
	if err != nil {
		return SampleResolution{}, err
	}
	resolution := SampleResolution{
		PagingModes:  SupportsPaging(profile.Provider.Type).String(),
		NativePaging: PagesNatively(profile.Provider.Type),
		Pageable:     true,
		Order:        order,
		Limits:       profile.RowLimits(),
	}
	if err := profile.Pageable(); err != nil {
		resolution.Pageable = false
		resolution.PageableReason = err.Error()
	}
	return resolution, nil
}
