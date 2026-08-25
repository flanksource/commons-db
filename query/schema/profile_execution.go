package schema

func traceSpec() Schema {
	return Schema{
		"type": "object", "title": "Trace",
		"description": "Long-running stream configuration",
		"properties": Schema{
			"maxDuration": Schema{
				"type": "string", "title": "Maximum duration",
				"description": "Session lifetime, for example 15m",
			},
			"maxEvents": Schema{
				"type": "integer", "title": "Maximum events", "minimum": 1,
				"description": "Final emitted rows retained in the session ring",
			},
			"buffer": Schema{
				"type": "object", "title": "Processor buffer",
				"description": "Collect raw rows before running whole-result processors; the first configured bound reached flushes the batch",
				"anyOf": []any{
					Schema{"required": []string{"maxRows"}},
					Schema{"required": []string{"maxWait"}},
				},
				"properties": Schema{
					"maxRows": Schema{
						"type": "integer", "title": "Maximum rows", "minimum": 1,
						"description": "Raw provider rows per processor batch",
					},
					"maxWait": Schema{
						"type": "string", "title": "Maximum wait",
						"description": "Time from the first pending raw row to a flush, for example 250ms",
					},
				},
			},
		},
	}
}
