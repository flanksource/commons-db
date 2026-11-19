package lease

// sqlSetHolder names the pinned session's holder for Holders, which reads it
// back from sys.dm_exec_sessions.
const sqlSetHolder = `
DECLARE @holder varbinary(128) = @p1;
SET CONTEXT_INFO @holder;
SELECT 0;`

const sqlHolderPermission = `SELECT HAS_PERMS_BY_NAME(NULL, NULL, N'VIEW SERVER STATE'), SUSER_SNAME();`

// sqlHolderLocks lists the granted session app locks whose resource
// description contains @p1. sys.dm_tran_locks shows only the first 32
// characters of an app-lock resource, so the caller matches the exact
// truncated names.
const sqlHolderLocks = `
SELECT l.request_session_id, l.request_mode, l.resource_description, s.context_info, s.login_time
FROM sys.dm_tran_locks AS l
JOIN sys.dm_exec_sessions AS s ON s.session_id = l.request_session_id
WHERE l.resource_type = N'APPLICATION' AND l.resource_database_id = DB_ID()
  AND l.request_owner_type = N'SESSION' AND l.request_status = N'GRANT'
  AND CHARINDEX(@p1, l.resource_description COLLATE Latin1_General_BIN2) > 0;`

const sqlRegisterWriter = `
DECLARE @status int, @gate nvarchar(255) = @p1 + N':gate';
EXEC @status = sys.sp_getapplock @Resource = @gate, @LockMode = N'IntentExclusive', @LockOwner = N'Session', @LockTimeout = 0;
SELECT @status;`

const sqlWaitingWriteLock = `
DECLARE @status int, @gate nvarchar(255) = @p1 + N':gate';
IF APPLOCK_MODE(N'public', @gate, N'Session') <> N'IntentExclusive' BEGIN SELECT -999; RETURN; END;
EXEC @status = sys.sp_getapplock @Resource = @p1, @LockMode = N'Exclusive', @LockOwner = N'Session', @LockTimeout = 0;
IF @status >= 0 EXEC @status = sys.sp_releaseapplock @Resource = @gate, @LockOwner = N'Session';
SELECT @status;`

const sqlTryWriteLock = `
DECLARE @status int, @gate nvarchar(255) = @p1 + N':gate';
EXEC @status = sys.sp_getapplock @Resource = @gate, @LockMode = N'Exclusive', @LockOwner = N'Session', @LockTimeout = 0;
IF @status < 0 BEGIN SELECT @status; RETURN; END;
EXEC @status = sys.sp_getapplock @Resource = @p1, @LockMode = N'Exclusive', @LockOwner = N'Session', @LockTimeout = 0;
IF @status >= 0 EXEC @status = sys.sp_releaseapplock @Resource = @gate, @LockOwner = N'Session';
SELECT @status;`

const sqlReadLock = `
DECLARE @status int, @gate nvarchar(255) = @p1 + N':gate';
EXEC @status = sys.sp_getapplock @Resource = @gate, @LockMode = N'Shared', @LockOwner = N'Session', @LockTimeout = 0;
IF @status < 0 BEGIN SELECT @status; RETURN; END;
EXEC @status = sys.sp_getapplock @Resource = @p1, @LockMode = N'Shared', @LockOwner = N'Session', @LockTimeout = 0;
IF @status < 0 BEGIN SELECT @status; RETURN; END;
DECLARE @i int = 1, @resource nvarchar(255), @mode nvarchar(32);
WHILE @i <= LEN(@p2)
BEGIN
    SET @resource = @p1 + N':group:' + CONVERT(nvarchar(8), @i);
    SET @mode = CASE SUBSTRING(@p2, @i, 1) WHEN N'0' THEN N'Shared' ELSE N'IntentExclusive' END;
    EXEC @status = sys.sp_getapplock @Resource = @resource, @LockMode = @mode, @LockOwner = N'Session', @LockTimeout = 0;
    IF @status < 0 BEGIN SELECT @status; RETURN; END;
    SET @i = @i + 1;
END;
EXEC @status = sys.sp_releaseapplock @Resource = @gate, @LockOwner = N'Session';
SELECT @status;`

const sqlUnlock = `
DECLARE @status int, @i int = LEN(@p2), @resource nvarchar(255);
WHILE @i > 0
BEGIN
    SET @resource = @p1 + N':group:' + CONVERT(nvarchar(8), @i);
    EXEC @status = sys.sp_releaseapplock @Resource = @resource, @LockOwner = N'Session';
    IF @status < 0 BEGIN SELECT @status; RETURN; END;
    SET @i = @i - 1;
END;
EXEC @status = sys.sp_releaseapplock @Resource = @p1, @LockOwner = N'Session';
SELECT @status;`
