package context

import (
	"strings"

	"github.com/flanksource/commons/har"
	"github.com/flanksource/commons/logger"
)

// HARMaxBodySizeDefault is the default per-entry body capture size for HAR.
const HARMaxBodySizeDefault = 64 * 1024

// annotationPrefixes are the accepted prefixes for observability annotations on
// context objects (e.g. "log.level", "mission-control/log.level").
var annotationPrefixes = []string{"", "mission-control/", "canary-checker/"}

func annotationValue(annotations map[string]string, key string) string {
	for _, prefix := range annotationPrefixes {
		if v, ok := annotations[prefix+key]; ok {
			return v
		}
	}
	return ""
}

// WithHARCollector returns a copy of the context carrying a shared HAR collector
// that HTTP-based providers and connections record requests into.
func (k Context) WithHARCollector(collector *har.Collector) Context {
	return k.WithValue("har-collector", collector)
}

// HARCollector returns the context-held HAR collector, or nil when none is set.
func (k Context) HARCollector() *har.Collector {
	if v, ok := k.Value("har-collector").(*har.Collector); ok {
		return v
	}
	return nil
}

// EffectiveLogLevel resolves the effective HTTP log level for a feature.
func (k Context) EffectiveLogLevel(feature string) (logger.LogLevel, string) {
	return k.effectiveObservabilityLevel(feature, false)
}

// EffectiveHARLevel resolves the effective HAR capture level for a feature.
func (k Context) EffectiveHARLevel(feature string) (logger.LogLevel, string) {
	return k.effectiveObservabilityLevel(feature, true)
}

// IsHARCaptureEnabled reports whether HAR capture is on for a feature.
func (k Context) IsHARCaptureEnabled(feature string) bool {
	level, _ := k.EffectiveHARLevel(feature)
	return level >= logger.Debug
}

// IsHTTPLoggingEnabled reports whether HTTP request logging is on for a feature.
func (k Context) IsHTTPLoggingEnabled(feature string) bool {
	level, _ := k.EffectiveLogLevel(feature)
	return level >= logger.Debug
}

// HTTPLoggingContent reports whether headers and bodies should be logged.
func (k Context) HTTPLoggingContent(feature string) (headers bool, bodies bool) {
	level, _ := k.EffectiveLogLevel(feature)
	return level >= logger.Debug, level >= logger.Trace
}

// HARConfig returns the HAR capture configuration for a feature.
func (k Context) HARConfig(feature string) har.HARConfig {
	cfg := har.DefaultConfig()
	cfg.MaxBodySize = int64(k.Properties().Int("har.maxBodySize", HARMaxBodySizeDefault))
	if v := k.Properties().String("har.captureContentTypes", ""); v != "" {
		cfg.CaptureContentTypes = splitCSV(v)
	}
	return cfg
}

// EffectiveHARCollector resolves which HAR collector to use for a feature. An
// explicit collector always wins; otherwise the context-owned collector is
// returned only when the feature's level is >= Debug.
func (k Context) EffectiveHARCollector(feature string, explicit *har.Collector) *har.Collector {
	if explicit != nil {
		return explicit
	}
	level, _ := k.EffectiveHARLevel(feature)
	if level < logger.Debug {
		return nil
	}
	return k.HARCollector()
}

func (k Context) effectiveObservabilityLevel(feature string, harCapture bool) (logger.LogLevel, string) {
	feature = strings.TrimSpace(strings.ToLower(feature))
	if feature == "" {
		feature = "http"
	}

	// Floor the level on the context logger and the global standard logger so a
	// global trace level reveals HTTP/HAR detail; per-feature keys raise further.
	var level logger.LogLevel
	if k.Logger != nil {
		level = normalizeFeatureLevel(k.Logger.GetLevel())
	}
	if std := logger.StandardLogger(); std != nil {
		level = maxLevel(level, std.GetLevel())
	}
	source := "logger"
	props := k.Properties()

	add := func(candidate logger.LogLevel, candidateSource string) {
		candidate = normalizeFeatureLevel(candidate)
		if candidate > level {
			level = candidate
			source = candidateSource
		}
	}
	addProperty := func(key string) {
		if v := props.String(key, ""); v != "" {
			add(logger.ParseLevel(k.Logger, v), key)
		}
	}
	addAnnotation := func(key string) {
		for _, o := range k.Objects() {
			annotations := getObjectMeta(o).Annotations
			if len(annotations) == 0 {
				continue
			}
			if v := annotationValue(annotations, key); v != "" {
				add(logger.ParseLevel(k.Logger, v), "annotation:"+key)
			}
		}
	}

	addProperty("log.level")
	addAnnotation("log.level")
	for _, o := range k.Objects() {
		annotations := getObjectMeta(o).Annotations
		if len(annotations) == 0 {
			continue
		}
		if annotationValue(annotations, "trace") == "true" {
			add(logger.Trace, "annotation:trace")
		} else if annotationValue(annotations, "debug") == "true" {
			add(logger.Debug, "annotation:debug")
		}
	}

	suffix := ""
	if harCapture {
		suffix = ".har"
	}
	addProperty("log.level.http" + suffix)
	for _, f := range featureLevelKeys(feature) {
		addProperty("log.level." + f + suffix)
	}
	addAnnotation("log.level.http" + suffix)
	for _, f := range featureLevelKeys(feature) {
		addAnnotation("log.level." + f + suffix)
	}

	return level, source
}

func featureLevelKeys(feature string) []string {
	if feature == "http" {
		return nil
	}
	switch feature {
	case "kubernetes":
		return []string{"kubernetes", "kubectl", "k8s"}
	default:
		return []string{feature}
	}
}

func normalizeFeatureLevel(level logger.LogLevel) logger.LogLevel {
	if level == logger.Silent || level < logger.Info {
		return logger.Info
	}
	return level
}

func maxLevel(a, b logger.LogLevel) logger.LogLevel {
	a = normalizeFeatureLevel(a)
	b = normalizeFeatureLevel(b)
	if a > b {
		return a
	}
	return b
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
