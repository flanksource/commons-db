DROP FUNCTION IF EXISTS check_summary_for_component;
DROP VIEW IF EXISTS check_summary;
DROP MATERIALIZED VIEW IF EXISTS check_status_summary;
DROP VIEW IF EXISTS check_status_summary_aged;
DROP VIEW IF EXISTS check_status_summary_hour;

CREATE OR REPLACE VIEW check_status_summary_hour as
  SELECT
    check_id,
    PERCENTILE_DISC(0.99) WITHIN GROUP (
      ORDER BY
        duration
    ) as p99,
    PERCENTILE_DISC(0.95) WITHIN GROUP (
      ORDER BY
        duration
    ) as p95,
      PERCENTILE_DISC(0.50) WITHIN GROUP (
      ORDER BY
        duration
    ) as p50,
    avg(duration) as mean,
    COUNT(*) FILTER (
      WHERE
        status = TRUE
    ) as passed,
    COUNT(*) FILTER (
      WHERE
        status = FALSE
    ) as failed,
    MAX(time) as last_check,
    MAX(time) FILTER (
      WHERE
        status = TRUE
    ) as last_pass,
    MAX(time) FILTER (
      WHERE
        status = FALSE
    ) as last_fail
  FROM
    check_statuses
  WHERE
    time > (NOW() at TIME ZONE 'utc' - Interval '1 hour')  GROUP BY
    check_id;

CREATE OR REPLACE VIEW check_status_summary_aged as
  SELECT DISTINCT ON (check_id) check_id,
  duration AS p99,
  duration as p95,
  duration AS p50,
  duration AS mean,
  CASE  WHEN check_statuses.status = TRUE THEN 1  ELSE 0 END AS passed,
  CASE  WHEN check_statuses.status = FALSE THEN 1  ELSE 0 END AS failed,
  time     AS last_check,
  CASE  WHEN check_statuses.status = TRUE THEN TIME   ELSE NULL END AS last_pass,
  CASE  WHEN check_statuses.status = FALSE THEN TIME  ELSE NULL END AS last_fail
  FROM   check_statuses
        inner join checks ON check_statuses.check_id = checks.id
  WHERE  checks.deleted_at IS NULL and check_id not in (select check_id from check_status_summary_hour)

  ORDER  BY check_id,
            TIME DESC;

CREATE  MATERIALIZED VIEW IF NOT EXISTS check_status_summary AS
  SELECT check_id, p99,p95, p50, mean, passed, failed, last_check, last_pass, last_fail from check_status_summary_hour
  UNION
  SELECT check_id, p99,p95, p50, mean, passed, failed, last_check, last_pass, last_fail from check_status_summary_aged;


CREATE OR REPLACE VIEW check_summary AS
  SELECT
    checks.id,
    checks.canary_id,
    json_build_object(
      'passed', check_status_summary.passed,
      'failed', check_status_summary.failed,
      'last_pass', check_status_summary.last_pass,
      'last_fail', check_status_summary.last_fail
    ) AS uptime,
    json_build_object('p99', check_status_summary.p99, 'p95', check_status_summary.p95, 'p50', check_status_summary.p50, 'avg', check_status_summary.mean) AS latency,
    checks.last_transition_time,
    checks.type,
    checks.icon,
    checks.name,
    checks.status,
    checks.description,
    canaries.namespace,
    canaries.name as canary_name,
    canaries.labels,
    checks.labels as check_labels,
    checks.severity,
    checks.owner,
    checks.last_runtime,
    checks.created_at,
    checks.updated_at,
    checks.deleted_at,
    checks.silenced_at
  FROM
    checks
    INNER JOIN canaries ON checks.canary_id = canaries.id
    LEFT JOIN check_status_summary ON checks.id = check_status_summary.check_id;

-- For last transition
CREATE OR REPLACE FUNCTION update_last_transition_time_for_check () RETURNS TRIGGER AS $$
BEGIN
    NEW.last_transition_time = NOW();
    RETURN NEW;
END
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER checks_last_transition_time BEFORE
UPDATE ON checks FOR EACH ROW WHEN (OLD.status IS DISTINCT FROM NEW.status)
EXECUTE PROCEDURE update_last_transition_time_for_check ();


CREATE OR REPLACE FUNCTION check_summary_for_component(id uuid) RETURNS setof check_summary
AS $$
  BEGIN
    RETURN QUERY
    SELECT check_summary.* FROM check_summary
    INNER JOIN check_component_relationships
      ON check_component_relationships.check_id = check_summary.id
    WHERE check_component_relationships.component_id = $1;
  END;
$$ language plpgsql;
