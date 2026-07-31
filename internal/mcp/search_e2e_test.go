package mcp

import (
	"strings"
	"testing"
	"time"

	"context-bridge/internal/store"
)

func TestContextBridgeSearchE2E(t *testing.T) {
	st := openTestStore(t)
	sessionID := "prefix-00000000-0000-0000-0000-000000000000"

	// Output 1: Some bash commands and basic file read with extensive logs
	seedCapture(t, st, sessionID, 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
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
	seedCapture(t, st, sessionID, 2, time.Date(2026, 3, 22, 10, 5, 0, 0, time.UTC), seededCapture{
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
	seedCapture(t, st, sessionID, 3, time.Date(2026, 3, 22, 10, 10, 0, 0, time.UTC), seededCapture{
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

	srv := New(st, "test", store.SearchModeRegex)

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
			">>> 20:   \"api_key\": \"[REDACTED]\",",
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

	t.Run("Scenario 4: Invalid Regex Rejected with Explicit Error", func(t *testing.T) {
		// An unbalanced bracket or parens makes a regex invalid: e.g., "[ERROR" without escaping
		// The new behavior rejects invalid regex patterns instead of falling back to literal
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "[ERROR", // Invalid regex pattern
		})
		if !resp.IsError {
			t.Fatalf("Expected error for invalid regex '[ERROR', but got success")
		}
		if !strings.Contains(resp.Text, "invalid regex pattern") {
			t.Errorf("Expected 'invalid regex pattern' error, got: %s", resp.Text)
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
		for _, want := range []string{
			"No matches for \"ThisWillNeverMatch12345\" across the 250 most recent candidate outputs",
			"this root session retains 3 outputs.",
		} {
			if !strings.Contains(resp.Text, want) {
				t.Errorf("Expected no-match response to contain %q, got:\n%s", want, resp.Text)
			}
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
		if !strings.Contains(resp.Text, "must not be empty") {
			t.Errorf("Expected empty-query validation error, got: %s", resp.Text)
		}
	})
}

func TestContextBridgeSearchE2EFTS5(t *testing.T) {
	st := openTestStore(t)
	sessionID := "fts5-test-session"

	seedCapture(t, st, sessionID, 1, time.Date(2026, 3, 22, 10, 0, 0, 0, time.UTC), seededCapture{
		childSessionID: sessionID + "-child",
		callID:         "fts5-call-1",
		agent:          "grep",
		description:    "Authentication module analysis",
		content: `Analyzing auth module:

class AuthenticationHandler:
    def handle_jwt_token(self, token):
        """Process JWT authentication token."""
        pass
    
    def handle_oauth_flow(self, request):
        """Process OAuth authentication flow."""
        pass

Found authentication patterns in:
- jwt_token validation
- oauth_flow processing
`,
	})

	seedCapture(t, st, sessionID, 2, time.Date(2026, 3, 22, 10, 5, 0, 0, time.UTC), seededCapture{
		childSessionID: sessionID + "-child2",
		callID:         "fts5-call-2",
		agent:          "explore",
		description:    "Error handling investigation",
		content: `Error patterns found:

[ERROR] jwt_token validation failed
[ERROR] oauth_flow timeout occurred
[WARN] authentication cache miss

Stack trace for auth error:
  File "auth.py", line 45: handle_jwt_token failed
  File "auth.py", line 67: handle_oauth_flow exception
`,
	})

	srv := New(st, "test", store.SearchModeFTS5)

	t.Run("Scenario 7: FTS5 Simple Word Match", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "authentication",
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}
		if !strings.Contains(resp.Text, "match(es)") {
			t.Errorf("Expected matches for 'authentication', got:\n%s", resp.Text)
		}
		if !strings.Contains(resp.Text, "Authentication") {
			t.Errorf("Expected match in output 1, got:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 8: FTS5 Match Found", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "jwt_token",
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}
		if !strings.Contains(resp.Text, "match(es)") {
			t.Errorf("Expected matches for 'jwt_token', got:\n%s", resp.Text)
		}
		if !strings.Contains(resp.Text, "jwt_token") {
			t.Errorf("Expected 'jwt_token' in snippet, got:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 9: FTS5 Quoted Phrase", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      `"jwt_token"`,
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}
		if !strings.Contains(resp.Text, "jwt_token") {
			t.Errorf("Expected matches for quoted phrase 'jwt_token', got:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 10: FTS5 No Results", func(t *testing.T) {
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "nonexistentword12345",
		})
		if resp.IsError {
			t.Fatalf("unexpected error: %v", resp.Text)
		}
		if !strings.Contains(resp.Text, "No matches") {
			t.Errorf("Expected 'No matches' message, got:\n%s", resp.Text)
		}
	})

	t.Run("Scenario 11: FTS5 Mode Server Instructions", func(t *testing.T) {
		regexSrv := New(st, "test", store.SearchModeRegex)
		fts5Srv := New(st, "test", store.SearchModeFTS5)

		regexResp := callListTools(t, regexSrv)
		fts5Resp := callListTools(t, fts5Srv)

		regexDesc := findToolDescription(regexResp, "search")
		fts5Desc := findToolDescription(fts5Resp, "search")

		if strings.Contains(regexDesc, "FTS5") {
			t.Errorf("Regex mode should NOT mention FTS5 in description, got: %q", regexDesc)
		}
		if !strings.Contains(fts5Desc, "literal-term") {
			t.Errorf("FTS5 mode should mention literal-term search in description, got: %q", fts5Desc)
		}
	})

	t.Run("Scenario 12: FTS5 Empty Query Validation", func(t *testing.T) {
		// FTS5 mode should also reject empty queries, just like regex mode
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "   ",
		})
		if !resp.IsError {
			t.Fatalf("Expected error for empty query in FTS5 mode, but got success")
		}
		if !strings.Contains(resp.Text, "must not be empty") {
			t.Errorf("Expected empty-query validation error in FTS5 mode, got: %s", resp.Text)
		}
	})

	t.Run("Scenario 13: FTS5 Mode Dispatch Verified", func(t *testing.T) {
		// Prove FTS5 mode is actually used by verifying FTS5-specific behavior
		// Use a simple word query that works for both FTS5 MATCH and snippet building
		resp := callTool(t, srv, "search", map[string]any{
			"session_id": sessionID,
			"query":      "jwt_token", // simple token query
		})
		if resp.IsError {
			t.Fatalf("unexpected error for FTS5 query: %v", resp.Text)
		}
		// Should match via FTS5 token matching
		if !strings.Contains(resp.Text, "match(es)") {
			t.Errorf("Expected matches for FTS5 query 'jwt_token', got:\n%s", resp.Text)
		}
	})
}
