UPDATE profiles
SET spec = jsonb_set(
  spec
    #- '{provider,options,kind}'
    #- '{provider,options,apiVersion}'
    #- '{provider,options,namespace}'
    #- '{provider,options,name}',
  '{query}',
  to_jsonb(
    CASE
      WHEN btrim(COALESCE(spec->>'query', '')) <> '' THEN spec->>'query'
      ELSE concat_ws(
        ' ',
        CASE WHEN NULLIF(spec#>>'{provider,options,kind}', '') IS NOT NULL
          THEN 'kind=' || (spec#>>'{provider,options,kind}') END,
        CASE WHEN NULLIF(spec#>>'{provider,options,namespace}', '') IS NOT NULL
          THEN 'namespace=' || (spec#>>'{provider,options,namespace}') END,
        CASE WHEN NULLIF(spec#>>'{provider,options,name}', '') IS NOT NULL
          THEN 'name=' || (spec#>>'{provider,options,name}') END
      )
    END
  ),
  true
),
updated_at = now()
WHERE spec#>>'{provider,type}' = 'k8s'
  AND (spec#>'{provider,options}') ?| ARRAY['kind', 'namespace', 'name'];
