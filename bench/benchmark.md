# PostgreSQL RLS Benchmark

## Running Benchmarks

query duration to fetch 10k, 25k, 50k and 100k config items in random are recorded.

```bash
make bench
```

## Results

Took ~23 minutes

```
goos: linux
goarch: amd64
pkg: github.com/flanksource/duty/bench
cpu: Intel(R) Core(TM) i9-14900K
BenchmarkMain/Sample-10000/catalog_changes/Without_RLS-32                   7210           1618398 ns/op
BenchmarkMain/Sample-10000/catalog_changes/With_RLS-32                       468          25696464 ns/op
BenchmarkMain/Sample-10000/config_changes/Without_RLS-32                    7225           1662744 ns/op
BenchmarkMain/Sample-10000/config_changes/With_RLS-32                        472          25400088 ns/op
BenchmarkMain/Sample-10000/config_detail/Without_RLS-32                     9204           1258744 ns/op
BenchmarkMain/Sample-10000/config_detail/With_RLS-32                        1110          10922483 ns/op
BenchmarkMain/Sample-10000/config_names/Without_RLS-32                      1630           7167192 ns/op
BenchmarkMain/Sample-10000/config_names/With_RLS-32                         1068          11294074 ns/op
BenchmarkMain/Sample-10000/config_summary/Without_RLS-32                     691          17307363 ns/op
BenchmarkMain/Sample-10000/config_summary/With_RLS-32                        134          88561638 ns/op
BenchmarkMain/Sample-10000/configs/Without_RLS-32                           5647           2071318 ns/op
BenchmarkMain/Sample-10000/configs/With_RLS-32                              1111          10701505 ns/op
BenchmarkMain/Sample-10000/analysis_types/Without_RLS-32                    9230           1267578 ns/op
BenchmarkMain/Sample-10000/analysis_types/With_RLS-32                       9554           1249278 ns/op
BenchmarkMain/Sample-10000/analyzer_types/Without_RLS-32                    9843           1186921 ns/op
BenchmarkMain/Sample-10000/analyzer_types/With_RLS-32                       9657           1208368 ns/op
BenchmarkMain/Sample-10000/change_types/Without_RLS-32                      7399           1602853 ns/op
BenchmarkMain/Sample-10000/change_types/With_RLS-32                         7578           1618275 ns/op
BenchmarkMain/Sample-10000/config_classes/Without_RLS-32                    9602           1053916 ns/op
BenchmarkMain/Sample-10000/config_classes/With_RLS-32                       1102          10601675 ns/op
BenchmarkMain/Sample-10000/config_types/Without_RLS-32                      9938           1220556 ns/op
BenchmarkMain/Sample-10000/config_types/With_RLS-32                         1132          10687988 ns/op

BenchmarkMain/Sample-25000/catalog_changes/Without_RLS-32                   3189           3777448 ns/op
BenchmarkMain/Sample-25000/catalog_changes/With_RLS-32                       199          59728301 ns/op
BenchmarkMain/Sample-25000/config_changes/Without_RLS-32                    3106           3796288 ns/op
BenchmarkMain/Sample-25000/config_changes/With_RLS-32                        202          59201120 ns/op
BenchmarkMain/Sample-25000/config_detail/Without_RLS-32                     3986           2825246 ns/op
BenchmarkMain/Sample-25000/config_detail/With_RLS-32                         472          25408187 ns/op
BenchmarkMain/Sample-25000/config_names/Without_RLS-32                       712          16344633 ns/op
BenchmarkMain/Sample-25000/config_names/With_RLS-32                          447          26696849 ns/op
BenchmarkMain/Sample-25000/config_summary/Without_RLS-32                     274          43747108 ns/op
BenchmarkMain/Sample-25000/config_summary/With_RLS-32                         45         242723303 ns/op
BenchmarkMain/Sample-25000/configs/Without_RLS-32                           2466           4885648 ns/op
BenchmarkMain/Sample-25000/configs/With_RLS-32                               470          25306394 ns/op
BenchmarkMain/Sample-25000/analysis_types/Without_RLS-32                    4042           2932464 ns/op
BenchmarkMain/Sample-25000/analysis_types/With_RLS-32                       4096           2936163 ns/op
BenchmarkMain/Sample-25000/analyzer_types/Without_RLS-32                    4293           2776851 ns/op
BenchmarkMain/Sample-25000/analyzer_types/With_RLS-32                       4180           2812037 ns/op
BenchmarkMain/Sample-25000/change_types/Without_RLS-32                      3093           3864532 ns/op
BenchmarkMain/Sample-25000/change_types/With_RLS-32                         3123           3806187 ns/op
BenchmarkMain/Sample-25000/config_classes/Without_RLS-32                    4693           2435089 ns/op
BenchmarkMain/Sample-25000/config_classes/With_RLS-32                        476          25211551 ns/op
BenchmarkMain/Sample-25000/config_types/Without_RLS-32                      4164           2861676 ns/op
BenchmarkMain/Sample-25000/config_types/With_RLS-32                          476          25352067 ns/op

BenchmarkMain/Sample-50000/catalog_changes/Without_RLS-32                   1560           7545395 ns/op
BenchmarkMain/Sample-50000/catalog_changes/With_RLS-32                       100         117274979 ns/op
BenchmarkMain/Sample-50000/config_changes/Without_RLS-32                    1573           7551748 ns/op
BenchmarkMain/Sample-50000/config_changes/With_RLS-32                         99         117770448 ns/op
BenchmarkMain/Sample-50000/config_detail/Without_RLS-32                     2101           5593338 ns/op
BenchmarkMain/Sample-50000/config_detail/With_RLS-32                         242          49418844 ns/op
BenchmarkMain/Sample-50000/config_names/Without_RLS-32                       378          31770900 ns/op
BenchmarkMain/Sample-50000/config_names/With_RLS-32                          226          52552379 ns/op
BenchmarkMain/Sample-50000/config_summary/Without_RLS-32                     128          90894472 ns/op
BenchmarkMain/Sample-50000/config_summary/With_RLS-32                         25         473002784 ns/op
BenchmarkMain/Sample-50000/configs/Without_RLS-32                           1251           9464835 ns/op
BenchmarkMain/Sample-50000/configs/With_RLS-32                               238          49838197 ns/op
BenchmarkMain/Sample-50000/analysis_types/Without_RLS-32                    2052           5801409 ns/op
BenchmarkMain/Sample-50000/analysis_types/With_RLS-32                       2121           5712487 ns/op
BenchmarkMain/Sample-50000/analyzer_types/Without_RLS-32                    2216           5442149 ns/op
BenchmarkMain/Sample-50000/analyzer_types/With_RLS-32                       2169           5515249 ns/op
BenchmarkMain/Sample-50000/change_types/Without_RLS-32                      1592           7552502 ns/op
BenchmarkMain/Sample-50000/change_types/With_RLS-32                         1521           7634041 ns/op
BenchmarkMain/Sample-50000/config_classes/Without_RLS-32                    2442           4780004 ns/op
BenchmarkMain/Sample-50000/config_classes/With_RLS-32                        241          49653432 ns/op
BenchmarkMain/Sample-50000/config_types/Without_RLS-32                      2145           5558880 ns/op
BenchmarkMain/Sample-50000/config_types/With_RLS-32                          241          49518770 ns/op

BenchmarkMain/Sample-100000/catalog_changes/Without_RLS-32                   668          15792969 ns/op
BenchmarkMain/Sample-100000/catalog_changes/With_RLS-32                       50         236585972 ns/op
BenchmarkMain/Sample-100000/config_changes/Without_RLS-32                    670          15857288 ns/op
BenchmarkMain/Sample-100000/config_changes/With_RLS-32                        49         237727030 ns/op
BenchmarkMain/Sample-100000/config_detail/Without_RLS-32                    1060          11282955 ns/op
BenchmarkMain/Sample-100000/config_detail/With_RLS-32                        121          98802558 ns/op
BenchmarkMain/Sample-100000/config_names/Without_RLS-32                      175          68280940 ns/op
BenchmarkMain/Sample-100000/config_names/With_RLS-32                         100         105502052 ns/op
BenchmarkMain/Sample-100000/config_summary/Without_RLS-32                     67         169628955 ns/op
BenchmarkMain/Sample-100000/config_summary/With_RLS-32                        12         984132710 ns/op
BenchmarkMain/Sample-100000/configs/Without_RLS-32                           609          19589287 ns/op
BenchmarkMain/Sample-100000/configs/With_RLS-32                              120          99833450 ns/op
BenchmarkMain/Sample-100000/analysis_types/Without_RLS-32                   1039          11434234 ns/op
BenchmarkMain/Sample-100000/analysis_types/With_RLS-32                      1064          11451964 ns/op
BenchmarkMain/Sample-100000/analyzer_types/Without_RLS-32                   1110          10675073 ns/op
BenchmarkMain/Sample-100000/analyzer_types/With_RLS-32                      1114          10854744 ns/op
BenchmarkMain/Sample-100000/change_types/Without_RLS-32                      669          15856671 ns/op
BenchmarkMain/Sample-100000/change_types/With_RLS-32                         668          16162332 ns/op
BenchmarkMain/Sample-100000/config_classes/Without_RLS-32                   1261           9487116 ns/op
BenchmarkMain/Sample-100000/config_classes/With_RLS-32                       121          98950319 ns/op
BenchmarkMain/Sample-100000/config_types/Without_RLS-32                     1060          11280585 ns/op
BenchmarkMain/Sample-100000/config_types/With_RLS-32                         121          99579524 ns/op
```
