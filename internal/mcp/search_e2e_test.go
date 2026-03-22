package mcp

import (
	"strings"
	"testing"
	"time"
)

func TestContextBridgeSearchE2E(t *testing.T) {
	st := openTestStore(t)
	sessionID := "prefix-00000000-0000-0000-0000-000000000000"

	// Output 1: Some bash commands and basic file read with extensive logs
	seedImportedCapture(t, st, sessionID, 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: sessionID + "-child1",
		callID:         "call-1",
		agent:          "executor",
		description:    "Run setup commands",
		content: `Running initial setup...
$ npm install
added 120 packages in 5s
fund 20 packages
audited 140 packages in 6s
found 0 vulnerabilities
$ cat config.json
{
  "api_key": "secret_abc123",
  "retry_count": 3,
  "timeout_ms": 5000,
  "database": {
    "host": "localhost",
    "port": 5432,
    "user": "admin"
  },
  "features": {
    "enable_auth": true,
    "enable_logging": true
  }
}
$ docker-compose up -d
Creating network "app_default" with the default driver
Creating volume "app_db_data" with default driver
Creating app_db_1 ... done
Creating app_web_1 ... done
Setup complete.`,
	})

	// Output 2: A python script with some code and a specific error, with more context
	seedImportedCapture(t, st, sessionID, 2, time.Date(2026, 3, 22, 10, 5, 0, 0, time.UTC), seededCapture{
		childSessionID: sessionID + "-child2",
		callID:         "call-2",
		agent:          "grep",
		description:    "Search for auth handler",
		content: `Found matches in multiple files:

src/utils/logger.py:
def log_error(msg):
    print(f"[ERROR] {msg}")

src/auth.py:
import os
import jwt
from datetime import datetime, timedelta

class AuthError(Exception):
    pass

class TokenExpiredError(AuthError):
    pass

def handle_authentication(req):
    """
    Authenticates the incoming request using JWT.
    """
    if not req.headers.get("Authorization"):
        raise AuthError("Missing credentials")
        
    token = req.headers["Authorization"].replace("Bearer ", "")
    
    try:
        payload = jwt.decode(token, os.getenv("JWT_SECRET"), algorithms=["HS256"])
        return validate_token(payload)
    except jwt.ExpiredSignatureError:
        raise TokenExpiredError("Token has expired")
    except jwt.InvalidTokenError:
        raise AuthError("Invalid token")

def validate_token(payload):
    if not payload.get("user_id"):
        return False
    return True
`,
	})

	// Output 3: System logs containing random patterns and timestamps, much longer
	seedImportedCapture(t, st, sessionID, 3, time.Date(2026, 3, 22, 10, 10, 0, 0, time.UTC), seededCapture{
		childSessionID: sessionID + "-child3",
		callID:         "call-3",
		agent:          "explore",
		description:    "Read server logs",
		content: `[INFO] 2026-03-22 10:09:01 Server starting on port 8080
[INFO] 2026-03-22 10:09:02 Loading configuration from config.json
[INFO] 2026-03-22 10:09:02 Connecting to database localhost:5432
[INFO] 2026-03-22 10:09:03 Database connection established
[WARN] 2026-03-22 10:09:05 Connection timeout from 192.168.1.50
[ERROR] 2026-03-22 10:09:06 AuthError: Missing credentials during login attempt
[DEBUG] 2026-03-22 10:09:06 Traceback (most recent call last):
  File "src/server.py", line 45, in handle_request
    user = handle_authentication(req)
  File "src/auth.py", line 22, in handle_authentication
    raise AuthError("Missing credentials")
[INFO] 2026-03-22 10:09:10 Restarting worker processes
[DEBUG] 2026-03-22 10:09:11 Stopping worker 4431
[DEBUG] 2026-03-22 10:09:12 Starting new worker
[INFO] 2026-03-22 10:09:15 Process 4432 (worker) initialized successfully
[WARN] 2026-03-22 10:09:20 High memory usage detected: 85%
[INFO] 2026-03-22 10:09:25 Garbage collection triggered
[INFO] 2026-03-22 10:09:30 Memory usage returned to normal: 45%
`,
	})

	srv := New(st, "test")

	t.Run("Scenario 1: Simple Regex Search with default context lines", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "AuthError.*Missing",
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}

		// Should match Output 2 (python script) and Output 3 (logs)
		if !strings.Contains(resp.Text, "3 match(es) across 2 outputs.") {
			t.Errorf("Expected 3 matches across 2 outputs, got:\n%s", resp.Text)
		}
		// Match in Output 2
		if !strings.Contains(resp.Text, "### #2 [grep] Search for auth handler") {
			t.Errorf("Expected output 2 in results, got:\n%s", resp.Text)
		}
		if !strings.Contains(resp.Text, ">>> 34:         raise AuthError(\"Missing credentials\")") {
			t.Errorf("Expected matched line 34 from Output 2, got:\n%s", resp.Text)
		}
		// Match in Output 3
		if !strings.Contains(resp.Text, "### #3 [explore] Read server logs") {
			t.Errorf("Expected output 3 in results, got:\n%s", resp.Text)
		}
		if !strings.Contains(resp.Text, ">>> 17: [ERROR] 2026-03-22 10:09:06 AuthError: Missing credentials during login attempt") {
			t.Errorf("Expected matched line 17 from Output 3, got:\n%s", resp.Text)
		}
		if !strings.Contains(resp.Text, ">>> 22:     raise AuthError(\"Missing credentials\")") {
			t.Errorf("Expected matched line 22 from Output 3 (traceback), got:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 2: Case-Insensitive Matching By Default", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "autherror",
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}

		// AuthError appears multiple times in Output 2 and Output 3
		if !strings.Contains(resp.Text, "6 match(es) across 2 outputs.") {
			t.Errorf("Expected 6 matches across 2 outputs, got:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 3: Regex with Custom Context Lines", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id":    sessionID,
			"query":         "api_key",
			"context_lines": 1,
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}

		// Should only show 1 line before and after
		expectedLines := []string{
			"   19: {",
			">>> 20:   \"api_key\": \"secret_abc123\",",
			"   21:   \"retry_count\": 3,",
		}
		for _, line := range expectedLines {
			if !strings.Contains(resp.Text, line) {
				t.Errorf("Expected snippet to contain %q, got:\n%s", line, resp.Text)
			}
		}
		// Should NOT contain line 18 or 22 because context_lines is 1
		if strings.Contains(resp.Text, "   18: $ cat config.json") {
			t.Errorf("Snippet contained line 18, but context_lines was 1:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 4: Invalid Regex Falls Back to Literal Match", func(t *testing.T) {
		// An unbalanced bracket or parens makes a regex invalid: e.g., "[ERROR" without escaping
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "[ERROR", // Invalid regex, but valid literal string
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}

		if !strings.Contains(resp.Text, "2 match(es) across 2 outputs.") {
			t.Errorf("Expected 2 literal match, got:\n%s", resp.Text)
		}
		if !strings.Contains(resp.Text, ">>> 16:     print(f\"[ERROR] {msg}\")") {
			t.Errorf("Expected matched line 16 from Output 2 with literal '[ERROR', got:\n%s", resp.Text)
		}
		if !strings.Contains(resp.Text, ">>> 17: [ERROR] 2026-03-22 10:09:06 AuthError") {
			t.Errorf("Expected matched line 17 from Output 3 with literal '[ERROR', got:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 5: Search yielding No Results", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "ThisWillNeverMatch12345",
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}
		if !strings.Contains(resp.Text, "No matches for \"ThisWillNeverMatch12345\" across 3 outputs.") {
			t.Errorf("Expected 'No matches...' message, got:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 6: Empty Query Validation", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "   ",
		})
		if !resp.IsError {
			t.Fatalf("Expected error for empty query, but got success")
		}
		if !strings.Contains(resp.Text, "query is required") {
			t.Errorf("Expected 'query is required' error, got: %s", resp.Text)
		}
	})
}
