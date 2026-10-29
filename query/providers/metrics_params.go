package providers

import "github.com/flanksource/commons-db/query"

func (prometheusProvider) ParameterizesQuery(cfg query.ProviderConfig) bool {
	return cfg.Options["language"] == "sql"
}
func (cloudwatchMetricsProvider) ParameterizesQuery(cfg query.ProviderConfig) bool {
	return cfg.Options["language"] == "sql"
}
func (prometheusProvider) ParameterizeQuery(req query.QueryParameterizationRequest) (query.ParameterizedQuery, error) {
	return metricParameters(req)
}
func (cloudwatchMetricsProvider) ParameterizeQuery(req query.QueryParameterizationRequest) (query.ParameterizedQuery, error) {
	return metricParameters(req)
}
func metricParameters(req query.QueryParameterizationRequest) (query.ParameterizedQuery, error) {
	p, err := query.GetProvider("sqlite")
	if err != nil {
		return query.ParameterizedQuery{}, err
	}
	return p.(query.QueryParameterizer).ParameterizeQuery(req)
}
