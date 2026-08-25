package inspect

import "time"

type CacheClass string

const (
	CacheClassOpenSearchTargets         CacheClass = "opensearch-targets"
	CacheClassSQLCatalog                CacheClass = "sql-catalog"
	CacheClassOpenSearchFields          CacheClass = "opensearch-fields"
	CacheClassOpenSearchDynamicMapping  CacheClass = "opensearch-dynamic-mapping"
	CacheClassOpenSearchConcreteMapping CacheClass = "opensearch-concrete-mapping"
	CacheClassCardinality               CacheClass = "column-cardinality"
	CacheClassFilterValues              CacheClass = "filter-values"
)

func Policy(class CacheClass) CachePolicy {
	switch class {
	case CacheClassOpenSearchTargets:
		return CachePolicy{
			Name: string(class), InitialFreshFor: 6 * time.Hour, MaximumFreshFor: 24 * time.Hour,
			FillTimeout: DefaultFillTimeout, MaxEntries: 256, MaxWeight: 100_000,
		}
	case CacheClassSQLCatalog:
		return CachePolicy{
			Name: string(class), InitialFreshFor: 24 * time.Hour, MaximumFreshFor: 7 * 24 * time.Hour,
			FillTimeout: DefaultFillTimeout, MaxEntries: 256, MaxWeight: 100_000,
		}
	case CacheClassOpenSearchFields:
		return CachePolicy{
			Name: string(class), InitialFreshFor: 24 * time.Hour, MaximumFreshFor: 7 * 24 * time.Hour,
			FillTimeout: DefaultFillTimeout, MaxEntries: 1_024, MaxWeight: 250_000,
		}
	case CacheClassOpenSearchDynamicMapping:
		return CachePolicy{
			Name: string(class), InitialFreshFor: 24 * time.Hour, MaximumFreshFor: 7 * 24 * time.Hour,
			FillTimeout: DefaultFillTimeout, MaxEntries: 1_024, MaxWeight: 250_000,
		}
	case CacheClassOpenSearchConcreteMapping:
		return CachePolicy{
			Name: string(class), InitialFreshFor: 7 * 24 * time.Hour, MaximumFreshFor: 30 * 24 * time.Hour,
			FillTimeout: DefaultFillTimeout, MaxEntries: 1_024, MaxWeight: 250_000,
		}
	case CacheClassCardinality:
		return CachePolicy{
			Name: string(class), InitialFreshFor: 7 * 24 * time.Hour, MaximumFreshFor: 30 * 24 * time.Hour,
			FillTimeout: DefaultFillTimeout, MaxEntries: 2_048, MaxWeight: 100_000,
		}
	case CacheClassFilterValues:
		return CachePolicy{
			Name: string(class), InitialFreshFor: 24 * time.Hour, MaximumFreshFor: 7 * 24 * time.Hour,
			FillTimeout: DefaultFillTimeout, MaxEntries: 4_096, MaxWeight: 100_000,
		}
	default:
		panic("unknown inspection cache class " + class)
	}
}
