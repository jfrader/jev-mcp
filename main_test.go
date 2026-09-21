package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const noulReply = `{"model":"jev-1.13.0","answers":{"noul":{"type":"noul","noul":0.95}},"usage":{"input_tokens":12,"output_tokens":3}}`

const mixedReply = `{"model":"jev-1.13.0","answers":{
	"route":{"type":"choice","choice":"technical","probabilities":{"technical":0.85,"billing":0.15},"confidence":0.78},
	"severity":{"type":"score","score":1.4,"legend":{"0":"minor","1":"major"},"probabilities":{"0":0.6,"1":0.4},"confidence":0.2}
},"usage":{"input_tokens":40,"output_tokens":9}}`

func withServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	apiKey = "test-key"
	baseURL = server.URL
	model = defaultModel
	return server
}

func reply(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, body)
	}
}

func runLines(t *testing.T, lines ...string) ([]rpcResponse, string) {
	t.Helper()
	var output bytes.Buffer
	var errors bytes.Buffer
	input := strings.Join(lines, "\n")
	if err := run(strings.NewReader(input), &output, &errors); err != nil {
		t.Fatal(err)
	}
	responses := []rpcResponse{}
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		if line == "" {
			continue
		}
		var response rpcResponse
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("invalid response %q: %v", line, err)
		}
		responses = append(responses, response)
	}
	return responses, errors.String()
}

func TestRunHandshake(t *testing.T) {
	withServer(t, reply(noulReply))
	responses, stderr := runLines(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"noul","arguments":{"state":"help now","instructions":"Does this convey urgency?"}}}`,
	)
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if len(responses) != 3 {
		t.Fatalf("expected 3 responses, got %d", len(responses))
	}
	for _, response := range responses {
		if response.Error != nil {
			t.Fatalf("unexpected RPC error: %s", response.Error.Message)
		}
	}
	if !strings.Contains(responses[1].String(), `"name":"ask"`) {
		t.Fatal("tools/list did not include ask")
	}
	text := toolText(t, responses[2])
	if !strings.Contains(text, `"noul": 0.95`) || !strings.Contains(text, `"yes": true`) {
		t.Fatalf("unexpected noul result: %s", text)
	}
}

func TestAskSendsEveryQuestionInOneRequest(t *testing.T) {
	var captured systemOneRequest
	var authorization string
	calls := 0
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		authorization = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("invalid request body: %v", err)
		}
		reply(mixedReply)(w, r)
	})

	result, err := toolAsk(json.RawMessage(`{
		"state": {"ticket": "Payouts failing for 3 days"},
		"questions": [
			{"id":"route","type":"choice","instructions":"Which team handles this?","criteria":{"technical":"bugs","billing":"payments"}},
			{"id":"severity","type":"score","instructions":"How bad is it?","levels":["minor","major"]}
		]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("expected one batched request, got %d", calls)
	}
	if authorization != "Bearer test-key" {
		t.Fatalf("unexpected authorization header: %q", authorization)
	}
	if len(captured.Questions) != 2 {
		t.Fatalf("expected 2 questions in one request, got %d", len(captured.Questions))
	}
	if captured.Questions["route"].Type != "choice" || captured.Questions["severity"].Type != "score" {
		t.Fatalf("unexpected question types: %#v", captured.Questions)
	}
	if !strings.Contains(string(captured.Questions["severity"].Criteria), "major") {
		t.Fatalf("score levels were not passed through: %s", captured.Questions["severity"].Criteria)
	}
	if captured.Model != defaultModel {
		t.Fatalf("unexpected model: %q", captured.Model)
	}
	if !bytes.Contains(captured.State, []byte("Payouts failing")) {
		t.Fatalf("state was not passed through: %s", captured.State)
	}

	output := result.(map[string]any)
	answers := output["answers"].(map[string]any)
	if answers["route"].(answerOut).Choice != "technical" {
		t.Fatalf("unexpected choice answer: %#v", answers["route"])
	}
	unreliable := output["unreliable"].([]string)
	if len(unreliable) != 1 || unreliable[0] != "severity" {
		t.Fatalf("expected severity to be flagged unreliable, got %#v", unreliable)
	}
	if output["model"] != "jev-1.13.0" {
		t.Fatalf("expected the versioned model id, got %#v", output["model"])
	}
}

func TestChooseAcceptsPlainOptionNames(t *testing.T) {
	var captured systemOneRequest
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &captured)
		reply(`{"model":"jev-1.13.0","answers":{"choice":{"type":"choice","choice":"local","probabilities":{"local":0.9,"cloud":0.1},"confidence":0.8}},"usage":{"input_tokens":9,"output_tokens":2}}`)(w, r)
	})

	result, err := toolChoose(json.RawMessage(`{"state":"one file question","instructions":"Which lane?","options":["local","cloud"]}`))
	if err != nil {
		t.Fatal(err)
	}
	criteria := string(captured.Questions["choice"].Criteria)
	if !strings.Contains(criteria, "local") || !strings.Contains(criteria, "cloud") {
		t.Fatalf("option names were not turned into criteria: %s", criteria)
	}
	output := result.(map[string]any)
	if output["choice"] != "local" {
		t.Fatalf("unexpected choice: %#v", output["choice"])
	}
	if reliable, ok := output["reliable"].(*bool); !ok || !*reliable {
		t.Fatalf("expected reliable true, got %#v", output["reliable"])
	}
}

func TestValidationRejectsBadQuestions(t *testing.T) {
	withServer(t, reply(mixedReply))
	cases := []struct {
		name    string
		call    func() (any, error)
		message string
	}{
		{
			name: "missing state",
			call: func() (any, error) {
				return toolAsk(json.RawMessage(`{"questions":[{"id":"a","type":"noul","instructions":"x?"}]}`))
			},
			message: "state is required",
		},
		{
			name: "unknown type",
			call: func() (any, error) {
				return toolAsk(json.RawMessage(`{"state":"s","questions":[{"id":"a","type":"vibe","instructions":"x?"}]}`))
			},
			message: "unknown type",
		},
		{
			name: "duplicate ids",
			call: func() (any, error) {
				return toolAsk(json.RawMessage(`{"state":"s","questions":[{"id":"a","type":"noul","instructions":"x?"},{"id":"a","type":"noul","instructions":"y?"}]}`))
			},
			message: "duplicate question id",
		},
		{
			name: "choice criteria as array",
			call: func() (any, error) {
				return toolAsk(json.RawMessage(`{"state":"s","questions":[{"id":"a","type":"choice","instructions":"x?","criteria":["a","b"]}]}`))
			},
			message: "object of option to description",
		},
		{
			name: "score with one level",
			call: func() (any, error) {
				return toolAsk(json.RawMessage(`{"state":"s","questions":[{"id":"a","type":"score","instructions":"x?","criteria":["only"]}]}`))
			},
			message: "accepts 2 to 10",
		},
		{
			name: "criteria given twice",
			call: func() (any, error) {
				return toolAsk(json.RawMessage(`{"state":"s","questions":[{"id":"a","type":"choice","instructions":"x?","criteria":{"a":"1"},"options":["b"]}]}`))
			},
			message: "provide the criteria once",
		},
		{
			name:    "score without levels",
			call:    func() (any, error) { return toolScore(json.RawMessage(`{"state":"s","instructions":"x?"}`)) },
			message: "criteria is required for a score",
		},
		{
			name:    "choose without options",
			call:    func() (any, error) { return toolChoose(json.RawMessage(`{"state":"s","instructions":"x?"}`)) },
			message: "options is required",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := testCase.call(); err == nil {
				t.Fatal("expected an error")
			} else if !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("expected error containing %q, got %q", testCase.message, err.Error())
			}
		})
	}
}

func TestStateTooLargeIsRejectedLocally(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("oversized state should not reach the API")
	})
	state, _ := json.Marshal(strings.Repeat("x", maxStateBytes+1))
	_, err := toolAsk(json.RawMessage(`{"state":` + string(state) + `,"questions":[{"id":"a","type":"noul","instructions":"x?"}]}`))
	if err == nil || !strings.Contains(err.Error(), "over the") {
		t.Fatalf("expected the state guard to fire, got %v", err)
	}
}

func TestRateLimitRetriesThenSucceeds(t *testing.T) {
	calls := 0
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":"slow down"}`)
			return
		}
		reply(noulReply)(w, r)
	})

	result, err := toolNoul(json.RawMessage(`{"state":"help","instructions":"Is this urgent?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("expected a retry after 429, got %d calls", calls)
	}
	if result.(map[string]any)["noul"].(float64) != 0.95 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestUnauthorizedExplainsTheKey(t *testing.T) {
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":"bad key"}`)
	})
	_, err := toolNoul(json.RawMessage(`{"state":"help","instructions":"Is this urgent?"}`))
	if err == nil || !strings.Contains(err.Error(), "TYPESAFE_API_KEY") {
		t.Fatalf("expected an actionable auth error, got %v", err)
	}
}

func TestOverloadIsRetried(t *testing.T) {
	calls := 0
	withServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= maxAttempts {
			w.WriteHeader(statusOverloaded)
			io.WriteString(w, `{"error":"overloaded"}`)
			return
		}
		reply(noulReply)(w, r)
	})
	if _, err := toolNoul(json.RawMessage(`{"state":"help","instructions":"Is this urgent?"}`)); err == nil {
		t.Fatal("expected overload to surface after the retry budget")
	}
	if calls != maxAttempts {
		t.Fatalf("expected %d attempts, got %d", maxAttempts, calls)
	}
}

func TestLoadConfigRequiresKey(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_BASE_URL", "")
	if err := loadConfig(); err == nil || !strings.Contains(err.Error(), "TYPESAFE_API_KEY") {
		t.Fatalf("expected a missing-key error, got %v", err)
	}
	t.Setenv("TYPESAFE_API_KEY", "k")
	t.Setenv("TYPESAFE_BASE_URL", "ftp://example.com")
	if err := loadConfig(); err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("expected a scheme error, got %v", err)
	}
	t.Setenv("TYPESAFE_BASE_URL", "https://api.typesafe.ai")
	t.Setenv("TYPESAFE_MODEL", "")
	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if model != defaultModel {
		t.Fatalf("expected the default model, got %q", model)
	}
}

func toolText(t *testing.T, response rpcResponse) string {
	t.Helper()
	encoded, err := json.Marshal(response.Result)
	if err != nil {
		t.Fatal(err)
	}
	var result toolResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected one content block, got %d", len(result.Content))
	}
	return result.Content[0].Text
}

func (response rpcResponse) String() string {
	encoded, _ := json.Marshal(response.Result)
	return string(encoded)
}
