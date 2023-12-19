DROP VIEW IF EXISTS integrations_with_status;

CREATE VIEW integrations_with_status AS
WITH combined AS (
SELECT
  id,
  NAME,
  description,
  'scrapers' AS integration_type,
  source,
  agent_id,
  created_at,
  updated_at,
  deleted_at,
  created_by,
  job_name,
  job_success_count,
  job_error_count,
  job_details,
  job_hostname,
  job_duration_millis,
  job_resource_type,
  job_status,
  job_time_start,
  job_time_end,
  job_created_at
FROM
  config_scrapers_with_status
UNION
SELECT
  id,
  NAME,
  '',
  'topologies' AS integration_type,
  source,
  agent_id,
  created_at,
  updated_at,
  deleted_at,
  created_by,
  job_name,
  job_success_count,
  job_error_count,
  job_details,
  job_hostname,
  job_duration_millis,
  job_resource_type,
  job_status,
  job_time_start,
  job_time_end,
  job_created_at
FROM
  topologies_with_status
UNION
SELECT
  id,
  NAME,
  '',
  'logging_backends' AS integration_type,
  source,
  agent_id,
  created_at,
  updated_at,
  deleted_at,
  created_by,
  '',
  0,
  0,
  NULL,
  '',
  0,
  NULL,
  '',
  NULL,
  NULL,
  NULL
FROM
  logging_backends
)
SELECT combined.*, people.name AS creator_name, people.avatar AS creator_avatar, people.title AS creator_title, people.email AS creator_email FROM combined LEFT JOIN people ON combined.created_by = people.id;