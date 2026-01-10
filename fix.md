# Code Review: Herald RSS-to-Email Project

---

# Herald - Issue Groups & Priorities

## **P0 - Critical (Security & Data Integrity)** ✅ COMPLETE

- ✅ #14: No Rate Limiting (SSH, SCP, web, email)
- ✅ #15: Token Generation (verify crypto/rand usage)

## **P1 - High (Performance & Reliability)** ✅ COMPLETE

- ✅ #8: N+1 Query Problem (batch operations)
- ✅ #26: No Cleanup of Old seen_items (6 month cleanup job)
- ✅ #23: Missing Graceful Shutdown for Scheduler (panic recovery)

## **P2 - Medium (Code Quality & UX)**

### Group A: Input Validation

- #16: No SMTP Auth Validation
- #27: No Feed Validation on Upload
- #37: No Cron Validation at Upload
- ✅ #36: No Max File Size on SCP Upload

### Group B: Performance Tuning

- #9: No Prepared Statements
- #10: Inefficient Sorting in Handlers
- #11: No HTTP Caching Headers

## **P3 - Low (Nice to Have)**

### Group C: Observability

- #24: No Metrics/Observability
- #35: HTTP Server Doesn't Log Requests
- #22: Inconsistent Logging Levels

### Group D: Architecture & Scalability

- #30: Scheduler Interval is Fixed
- #31: No Pagination on Feed Endpoints

### Group E: Code Hygiene

- #3: Context Timeout Duplication
- #19: Magic Numbers
- #18: Error Wrapping Inconsistency
- #21: Unused Context Parameter
- #33-34: Minor Code Cleanup

### Group F: Documentation

- #28: Inconsistent Command Help
- #29: Config Example Doesn't Match Defaults

### Group G: Testing

- #20: No Tests

---

## **Critical Issues - COMPLETED** ✅

### 1. **Database Connection Pool Not Configured** ✅

**Fixed:** Added WAL mode, busy timeout, and connection pool limits in `store/db.go:16-18`

### 2. **Code Duplication in Scheduler** ✅

**Fixed:** Refactored into shared `collectNewItems` and `sendDigestAndMarkSeen` helper methods

### 3. **No Context Timeout on HTTP Requests** 🔄

**Location:** `scheduler/fetch.go:41-43`

While you set a 30s timeout on the context, the HTTP client also has a separate 30s timeout. If the context times out first, the HTTP client won't respect it immediately.

**Fix:** Use context-aware client without separate timeout.

### 4. **Missing Index on Configs Active Status** ✅

**Fixed:** Added partial index in `store/db.go:93`

### 5. **Race Condition in seen_items** ✅

**Fixed:** Using transactions to mark items seen before email send

### 6. **Unbounded Memory Growth in Feed Fetching** ✅

**Fixed:** Added semaphore limiting to 10 concurrent fetches in `scheduler/fetch.go`

### 7. **Silent Failure on Email Send** ✅

**Fixed:** Items only marked seen after successful email via transaction commit

### 14. **No Rate Limiting** ✅

**Fixed:** Added comprehensive rate limiting using `golang.org/x/time/rate`:
- Created reusable `ratelimit.Limiter` with token bucket algorithm (`ratelimit/limiter.go`)
- Web handler middleware: 10 req/sec, burst of 20 per IP (`web/server.go:65-77`)
- SSH authentication: 5 req/sec, burst of 10 per fingerprint (`ssh/server.go:96-101`)
- SCP uploads: 5 req/sec, burst of 10 per user (`ssh/scp.go:107-110`)
- Email sending: 1 per minute per user (`scheduler/scheduler.go:207-210`)
- Added 1MB max file size limit for SCP uploads (`ssh/scp.go:112-115`)
- Rate limiter automatically cleans up inactive entries every 5 minutes

### 15. **Token Generation Not Cryptographically Secure** ✅

**Already secure:** Confirmed using `crypto/rand` in `store/unsubscribe.go:14`

---

## **Performance Issues**

### 8. **N+1 Query Problem** ✅

**Location:** Multiple locations

- `web/handlers.go:99-103` - Gets feeds for each config in a loop
- `scheduler/scheduler.go` - Checks each item individually for seen status

**Fixed:** 
- Added `GetSeenGUIDs()` batch method in `store/items.go` to check multiple items at once
- Added `GetFeedsByConfigs()` batch method in `store/feeds.go` to fetch feeds for multiple configs
- Updated `scheduler/scheduler.go:collectNewItems()` to use batch GUID checking
- Updated `web/handlers.go` dashboard handler to batch fetch all feeds

### 9. **No Prepared Statements** 📝

**Location:** All store methods

Every query uses `QueryContext`/`ExecContext` with raw SQL strings. These are reparsed on every call.

**Fix:** Use prepared statements for frequently called queries (IsItemSeen, MarkItemSeen, etc.).

### 10. **Inefficient Sorting in Handlers** 🔢

**Location:** `web/handlers.go:231-235` and `326-330`

You sort items by parsing time strings in a comparison function. This parses the same timestamps multiple times.

**Fix:** Parse once, sort by parsed time, or use database ORDER BY.

### 11. **No HTTP Caching Headers** 🌐

**Location:** `web/handlers.go` - all feed handlers

RSS/JSON feeds don't set `Cache-Control`, `ETag`, or `Last-Modified` headers. Every request fetches from DB.

**Fix:** Add caching headers:

```go
w.Header().Set("Cache-Control", "public, max-age=300")
w.Header().Set("ETag", fmt.Sprintf(`"%s-%d"`, fingerprint, cfg.LastRun.Time.Unix()))
```

### 12. **Database Migration Runs on Every Connection** 🔄

**Location:** `store/db.go:26-28`

Migration runs inside `Open()`, which happens once at startup. However, `Migrate()` is also exposed and called separately in `main.go:160`. The schema execution uses `CREATE TABLE IF NOT EXISTS` which is fine, but it's still unnecessary work.

---

## **Security Issues**

### 13. **Missing Input Validation on Email Addresses** ✅

**Already implemented:** Using `net/mail.ParseAddress()` in `config/validate.go:24-26`

### 16. **No SMTP Auth Validation** 🔒

**Location:** `email/send.go:102-105`

SMTP auth is optional (`if m.cfg.User != "" && m.cfg.Pass != ""`). Many SMTP servers require auth, and this silently continues without it.

**Fix:** Validate SMTP config at startup.

### 17. **SQL Injection Potential in UPSERT** 💉

**Location:** `store/items.go:29-33`

While using parameterized queries (good!), the `ON CONFLICT` clause should explicitly name the conflict target for clarity and safety:

```sql
ON CONFLICT(feed_id, guid) DO UPDATE SET ...
```

(Actually you already do this correctly, but worth noting for other queries)

---

## **Code Quality Issues**

### 18. **Error Wrapping Inconsistency** 🎁

Some functions use `fmt.Errorf("verb: %w", err)`, others use `fmt.Errorf("verb %w", err)` (no colon). Inconsistent style makes logs harder to parse.

### 19. **Magic Numbers** 🎩

**Location:** Multiple

- `scheduler/scheduler.go:84` - hardcoded 3 months
- `scheduler/scheduler.go:148-150` - hardcoded 5 items threshold
- `web/handlers.go:238` and `332` - hardcoded 100 items limit
- `scheduler/fetch.go:41` - hardcoded 30s timeout

**Fix:** Extract to constants or config.

### 20. **No Tests** 🧪

**Location:** Entire codebase

Zero test coverage. Critical business logic (cron parsing, config parsing, email rendering) is untested.

### 21. **Unused Context Parameter** 🗑️

**Location:** `store/db.go:109-111`

```go
func (db *DB) Migrate(ctx context.Context) error {
    return db.migrate() // ctx is ignored
}
```

Either remove the context parameter or pass it to a context-aware migrate function.

### 22. **Inconsistent Logging Levels** 📝

Some errors are `logger.Error`, some are `logger.Warn`. For example, feed fetch errors are `Warn` (line 89 of scheduler.go) but other errors are `Error`. Establish consistent criteria.

### 23. **Missing Graceful Shutdown for Scheduler** ✅

**Location:** `main.go:194-197`

**Fixed:**
- Added panic recovery with defer in `scheduler/scheduler.go:tick()` 
- Added panic recovery wrapper in `main.go` scheduler goroutine
- Added panic recovery in cleanup job

---

## **Missing Features**

### 24. **No Metrics/Observability** 📈

No Prometheus metrics, no health check endpoint, no structured logging for monitoring. For a long-running service, this is critical.

### 25. **No Email Validation on Successful Send** ✅

You log `"email sent"` but don't verify SMTP actually accepted it (some SMTP servers queue and fail later).

### 26. **No Cleanup of Old seen_items** ✅

**Fixed:**
- Added `CleanupOldSeenItems()` method in `store/items.go` to delete items older than specified duration
- Added cleanup ticker in `scheduler/scheduler.go:Start()` that runs every 24 hours
- Cleanup runs on startup and then daily, removing items older than 6 months
- Added logging for cleanup operations showing number of items deleted

### 27. **No Feed Validation on Upload** 🔍

When a user uploads a config with feed URLs, you don't validate the URLs are actually RSS/Atom feeds. First run will fail.

**Fix:** Optionally fetch and validate feeds on upload (with short timeout).

---

## **Documentation Issues**

### 28. **Inconsistent Command Help** 📖

`ssh/server.go:160-165` shows welcome message with command list, but the actual commands are in `ssh/commands.go` (not reviewed in detail). These could drift out of sync.

### 29. **Config Example Doesn't Match Defaults** ⚙️

`main.go:88` shows `inline: false` as default in comment, but `config/parse.go:27` sets default to `false`, and `README.md:89` says default is `true`.

**Fix:** Align all documentation with actual code defaults.

---

## **Architectural Concerns**

### 30. **Scheduler Interval is Fixed** ⏲️

**Location:** `main.go:172`

60-second interval is hardcoded. This doesn't scale well—if you have thousands of users, checking every 60 seconds is wasteful. Consider event-driven scheduling with a priority queue.

### 31. **No Pagination on Feed Endpoints** 📄

**Location:** `web/handlers.go:238` and `332`

Hardcoded limit of 100 items. Users can't access older items.

### 32. **No Transaction for Config Update** ✅

**Fixed:** Config upload now uses transactions in `ssh/scp.go:134-161`

---

## **Minor Issues**

33. **Unused `getCommitHash()` function** - `main.go:127-140` - function defined but only used in one place, could be inlined
34. **Inconsistent fingerprint shortening** - Sometimes 12 chars, sometimes 7 chars
35. **HTTP server doesn't log requests** - No request logging middleware
36. ✅ **No max file size on SCP upload** - Fixed: 1MB limit in `ssh/scp.go:112-115`
37. **No validation on cron expressions at upload time** - Invalid cron is only caught on first run

---

## **Positive Notes** ✅

- Good use of Context for cancellation
- Proper use of foreign keys and CASCADE
- Clean separation of concerns (store/scheduler/ssh/web)
- Good use of Charm libraries
- ETag/Last-Modified support for feed fetching
- Unsubscribe functionality implemented
- SQL injection protection with parameterized queries
- Config file validation before accepting uploads

---

## **Priority Fixes**

### **High Priority (Fix ASAP):**

1. ✅ Database connection pool configuration (#1)
2. ✅ Race condition in seen_items (#5)
3. ✅ Silent failure on email send (#7)
4. ✅ No rate limiting (#14)
5. ✅ No transaction for config updates (#32)
6. ✅ Token generation security (#15)
7. ✅ Max file size on SCP upload (#36)

### **Medium Priority:**

6. ✅ Code duplication in scheduler (#2)
7. N+1 query problems (#8)
8. ✅ Unbounded feed fetching concurrency (#6)
9. ✅ Missing input validation (#13)
10. No cleanup of old data (#26)

### **Low Priority (Technical Debt):**

11. Add tests (#20)
12. Add metrics (#24)
13. Extract magic numbers (#19)
14. Add HTTP caching (#11)
