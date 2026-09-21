package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL       = "https://api.typesafe.ai"
	defaultModel         = "jev-latest"
	serverVersion        = "0.1.0"
	protocolVersion      = "2024-11-05"
	defaultMinConfidence = 0.5
	maxStateBytes        = 120000
	maxAttempts          = 3
	maxChoiceOptions     = 255
	minScoreLevels       = 2
	maxScoreLevels       = 10
	statusOverloaded     = 529
)

var (
	apiKey  string
	baseURL string
	model   string
	client  = &http.Client{Timeout: 30 * time.Second}
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolDef struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(json.RawMessage) (any, error)
}

type apiQuestion struct {
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
}

type systemOneRequest struct {
	State     json.RawMessage        `json:"state"`
	Model     string                 `json:"model"`
	Questions map[string]apiQuestion `json:"questions"`
}

type tokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type systemOneResponse struct {
	Model   string                     `json:"model"`
	Answers map[string]json.RawMessage `json:"answers"`
	Usage   tokenUsage                 `json:"usage"`
}

type choiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type scoreAnswer struct {
	Type          string             `json:"type"`
	Score         float64            `json:"score"`
	Legend        map[string]string  `json:"legend"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type noulAnswer struct {
	Type string  `json:"type"`
	Noul float64 `json:"noul"`
}

type questionArg struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
	Options      json.RawMessage `json:"options,omitempty"`
	Levels       json.RawMessage `json:"levels,omitempty"`
}

type askArgs struct {
	State         json.RawMessage `json:"state"`
	Questions     []questionArg   `json:"questions"`
	MinConfidence *float64        `json:"min_confidence,omitempty"`
}

type chooseArgs struct {
	State         json.RawMessage `json:"state"`
	Instructions  json.RawMessage `json:"instructions"`
	Options       json.RawMessage `json:"options"`
	MinConfidence *float64        `json:"min_confidence,omitempty"`
}

type scoreArgs struct {
	State         json.RawMessage `json:"state"`
	Instructions  json.RawMessage `json:"instructions"`
	Levels        json.RawMessage `json:"levels"`
	MinConfidence *float64        `json:"min_confidence,omitempty"`
}

type noulArgs struct {
	State        json.RawMessage `json:"state"`
	Instructions json.RawMessage `json:"instructions"`
	Criteria     json.RawMessage `json:"criteria,omitempty"`
	Threshold    *float64        `json:"threshold,omitempty"`
}

type answerOut struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Reliable      *bool              `json:"reliable,omitempty"`
	Noul          *float64           `json:"noul,omitempty"`
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func loadConfig() error {
	apiKey = strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	baseURL = strings.TrimRight(envOr("TYPESAFE_BASE_URL", defaultBaseURL), "/")
	model = envOr("TYPESAFE_MODEL", defaultModel)

	if apiKey == "" {
		return errors.New("TYPESAFE_API_KEY is required (create a key at https://console.typesafe.ai/keys)")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("TYPESAFE_BASE_URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("TYPESAFE_BASE_URL must use http or https")
	}
	return nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n...(truncated)"
}

func floatOr(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	if *value < 0 {
		return 0
	}
	if *value > 1 {
		return 1
	}
	return *value
}

func checkState(state json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(state)) == 0 {
		return nil, errors.New("state is required")
	}
	if len(state) > maxStateBytes {
		return nil, fmt.Errorf("state is %d bytes, over the %d-byte guard; jev-1.13 allows ~32k tokens for state plus the longest question, and accuracy drops on unrelated detail, so filter state in code and send only what the questions need", len(state), maxStateBytes)
	}
	return state, nil
}

func postSystemOne(payload []byte) (*systemOneResponse, time.Duration, bool, error) {
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/systemone", bytes.NewReader(payload))
	if err != nil {
		return nil, 0, false, err
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "jev-mcp/"+serverVersion)

	response, err := client.Do(request)
	if err != nil {
		return nil, 0, false, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, 0, false, err
	}

	if response.StatusCode >= 200 && response.StatusCode < 300 {
		var result systemOneResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, 0, false, fmt.Errorf("decode response: %w", err)
		}
		return &result, -1, false, nil
	}

	switch response.StatusCode {
	case http.StatusUnauthorized:
		return nil, 0, false, errors.New("HTTP 401: TYPESAFE_API_KEY is missing or invalid")
	case http.StatusTooManyRequests, statusOverloaded:
		wait := time.Duration(-1)
		if header := strings.TrimSpace(response.Header.Get("Retry-After")); header != "" {
			if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
				wait = time.Duration(seconds) * time.Second
			}
		}
		return nil, wait, true, fmt.Errorf("HTTP %d: %s", response.StatusCode, truncate(string(body), 300))
	default:
		return nil, 0, false, fmt.Errorf("HTTP %d: %s", response.StatusCode, truncate(string(body), 300))
	}
}

func callSystemOne(state json.RawMessage, questions map[string]apiQuestion) (*systemOneResponse, time.Duration, error) {
	payload, err := json.Marshal(systemOneRequest{State: state, Model: model, Questions: questions})
	if err != nil {
		return nil, 0, err
	}

	start := time.Now()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, wait, retryable, err := postSystemOne(payload)
		if err == nil {
			return result, time.Since(start), nil
		}
		lastErr = err
		if !retryable {
			break
		}
		if attempt == maxAttempts {
			break
		}
		if wait < 0 {
			wait = 500 * time.Millisecond << (attempt - 1)
		}
		if wait > 15*time.Second {
			wait = 15 * time.Second
		}
		time.Sleep(wait)
	}
	return nil, 0, lastErr
}

func checkInstructions(instructions json.RawMessage) error {
	if len(bytes.TrimSpace(instructions)) == 0 {
		return errors.New("instructions is required")
	}
	return nil
}

func criteriaFor(question questionArg) (json.RawMessage, error) {
	given := 0
	for _, candidate := range []json.RawMessage{question.Criteria, question.Options, question.Levels} {
		if len(bytes.TrimSpace(candidate)) > 0 {
			given++
		}
	}
	if given > 1 {
		return nil, errors.New("provide the criteria once, not as criteria plus options or levels")
	}
	criteria := question.Criteria
	if len(bytes.TrimSpace(criteria)) == 0 {
		criteria = question.Options
	}
	if len(bytes.TrimSpace(criteria)) == 0 {
		criteria = question.Levels
	}
	return criteria, nil
}

func checkCriteria(kind string, criteria json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(criteria)
	if len(trimmed) == 0 {
		switch kind {
		case "choice":
			return nil, errors.New("criteria is required for a choice: an object of option to description")
		case "score":
			return nil, errors.New("criteria is required for a score: an array of 2 to 10 level descriptions")
		default:
			return nil, nil
		}
	}

	switch kind {
	case "choice":
		var options map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &options); err != nil {
			return nil, errors.New("a choice asks for options, so criteria must be an object of option to description (or options as an array of names)")
		}
		if len(options) == 0 {
			return nil, errors.New("criteria must define at least one option")
		}
		if len(options) > maxChoiceOptions {
			return nil, fmt.Errorf("criteria has %d options, over the %d option limit", len(options), maxChoiceOptions)
		}
		return json.RawMessage(trimmed), nil
	case "score":
		var levels []json.RawMessage
		if err := json.Unmarshal(trimmed, &levels); err != nil {
			return nil, errors.New("a score asks for ordered levels, so criteria must be an array of level descriptions")
		}
		if len(levels) < minScoreLevels || len(levels) > maxScoreLevels {
			return nil, fmt.Errorf("criteria has %d levels; a score accepts %d to %d", len(levels), minScoreLevels, maxScoreLevels)
		}
		return json.RawMessage(trimmed), nil
	case "noul":
		var pair map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &pair); err != nil {
			return nil, errors.New("noul criteria must be an object describing what true and false mean")
		}
		return json.RawMessage(trimmed), nil
	default:
		return nil, fmt.Errorf("unknown question type %q: use noul, choice, or score", kind)
	}
}

func buildQuestion(question questionArg) (string, apiQuestion, error) {
	id := strings.TrimSpace(question.ID)
	if id == "" {
		return "", apiQuestion{}, errors.New("every question needs an id")
	}
	kind := strings.ToLower(strings.TrimSpace(question.Type))
	switch kind {
	case "noul", "choice", "score":
	default:
		return "", apiQuestion{}, fmt.Errorf("question %q has unknown type %q: use noul, choice, or score", id, question.Type)
	}
	if err := checkInstructions(question.Instructions); err != nil {
		return "", apiQuestion{}, fmt.Errorf("question %q: %w", id, err)
	}
	criteria, err := criteriaFor(question)
	if err != nil {
		return "", apiQuestion{}, fmt.Errorf("question %q: %w", id, err)
	}
	criteria, err = checkCriteria(kind, criteria)
	if err != nil {
		return "", apiQuestion{}, fmt.Errorf("question %q: %w", id, err)
	}
	return id, apiQuestion{Type: kind, Instructions: question.Instructions, Criteria: criteria}, nil
}

func decodeAnswer(id string, raw json.RawMessage, minConfidence float64) (answerOut, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return answerOut{}, fmt.Errorf("answer %q: %w", id, err)
	}

	switch probe.Type {
	case "choice":
		var answer choiceAnswer
		if err := json.Unmarshal(raw, &answer); err != nil {
			return answerOut{}, fmt.Errorf("answer %q: %w", id, err)
		}
		reliable := answer.Confidence >= minConfidence
		return answerOut{
			Type:          "choice",
			Choice:        answer.Choice,
			Probabilities: answer.Probabilities,
			Confidence:    &answer.Confidence,
			Reliable:      &reliable,
		}, nil
	case "score":
		var answer scoreAnswer
		if err := json.Unmarshal(raw, &answer); err != nil {
			return answerOut{}, fmt.Errorf("answer %q: %w", id, err)
		}
		reliable := answer.Confidence >= minConfidence
		return answerOut{
			Type:          "score",
			Score:         &answer.Score,
			Legend:        answer.Legend,
			Probabilities: answer.Probabilities,
			Confidence:    &answer.Confidence,
			Reliable:      &reliable,
		}, nil
	case "noul":
		var answer noulAnswer
		if err := json.Unmarshal(raw, &answer); err != nil {
			return answerOut{}, fmt.Errorf("answer %q: %w", id, err)
		}
		return answerOut{Type: "noul", Noul: &answer.Noul}, nil
	default:
		return answerOut{}, fmt.Errorf("answer %q: unexpected type %q", id, probe.Type)
	}
}

func runQuestions(state json.RawMessage, questions map[string]apiQuestion, minConfidence float64) (map[string]any, error) {
	result, elapsed, err := callSystemOne(state, questions)
	if err != nil {
		return nil, err
	}

	answers := map[string]any{}
	unreliable := []string{}
	for id, raw := range result.Answers {
		answer, err := decodeAnswer(id, raw, minConfidence)
		if err != nil {
			return nil, err
		}
		if answer.Reliable != nil && !*answer.Reliable {
			unreliable = append(unreliable, id)
		}
		answers[id] = answer
	}
	if len(answers) == 0 {
		return nil, errors.New("the API returned no answers")
	}

	output := map[string]any{
		"model":      result.Model,
		"answers":    answers,
		"usage":      result.Usage,
		"latency_ms": elapsed.Milliseconds(),
	}
	if len(unreliable) > 0 {
		output["unreliable"] = unreliable
	}
	return output, nil
}

func toolAsk(raw json.RawMessage) (any, error) {
	var args askArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("bad arguments: %w", err)
	}
	state, err := checkState(args.State)
	if err != nil {
		return nil, err
	}
	if len(args.Questions) == 0 {
		return nil, errors.New("questions must contain at least one question")
	}

	questions := map[string]apiQuestion{}
	for _, question := range args.Questions {
		id, built, err := buildQuestion(question)
		if err != nil {
			return nil, err
		}
		if _, exists := questions[id]; exists {
			return nil, fmt.Errorf("duplicate question id %q", id)
		}
		questions[id] = built
	}
	return runQuestions(state, questions, floatOr(args.MinConfidence, defaultMinConfidence))
}

func optionsToCriteria(options json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(options)
	if len(trimmed) == 0 {
		return nil, errors.New("options is required: an object of option to description, or an array of option names")
	}
	if trimmed[0] != '[' {
		return json.RawMessage(trimmed), nil
	}
	var names []json.RawMessage
	if err := json.Unmarshal(trimmed, &names); err != nil {
		return nil, errors.New("options must be an object of option to description, or an array of option names")
	}
	if len(names) == 0 {
		return nil, errors.New("options must define at least one option")
	}
	described := map[string]any{}
	for _, name := range names {
		var label string
		if err := json.Unmarshal(name, &label); err != nil {
			return nil, errors.New("an array of options must contain only option names as strings")
		}
		label = strings.TrimSpace(label)
		if label == "" {
			return nil, errors.New("option names cannot be empty")
		}
		described[label] = nil
	}
	encoded, err := json.Marshal(described)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(encoded), nil
}

func toolChoose(raw json.RawMessage) (any, error) {
	var args chooseArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("bad arguments: %w", err)
	}
	state, err := checkState(args.State)
	if err != nil {
		return nil, err
	}
	if err := checkInstructions(args.Instructions); err != nil {
		return nil, err
	}
	criteria, err := optionsToCriteria(args.Options)
	if err != nil {
		return nil, err
	}
	criteria, err = checkCriteria("choice", criteria)
	if err != nil {
		return nil, err
	}

	minConfidence := floatOr(args.MinConfidence, defaultMinConfidence)
	output, err := runQuestions(state, map[string]apiQuestion{
		"choice": {Type: "choice", Instructions: args.Instructions, Criteria: criteria},
	}, minConfidence)
	if err != nil {
		return nil, err
	}

	answer, ok := output["answers"].(map[string]any)["choice"].(answerOut)
	if !ok {
		return nil, errors.New("the API returned no choice answer")
	}
	return map[string]any{
		"model":          output["model"],
		"choice":         answer.Choice,
		"probabilities":  answer.Probabilities,
		"confidence":     answer.Confidence,
		"reliable":       answer.Reliable,
		"min_confidence": minConfidence,
		"latency_ms":     output["latency_ms"],
		"usage":          output["usage"],
	}, nil
}

func toolScore(raw json.RawMessage) (any, error) {
	var args scoreArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("bad arguments: %w", err)
	}
	state, err := checkState(args.State)
	if err != nil {
		return nil, err
	}
	if err := checkInstructions(args.Instructions); err != nil {
		return nil, err
	}
	criteria, err := checkCriteria("score", args.Levels)
	if err != nil {
		return nil, err
	}

	minConfidence := floatOr(args.MinConfidence, defaultMinConfidence)
	output, err := runQuestions(state, map[string]apiQuestion{
		"score": {Type: "score", Instructions: args.Instructions, Criteria: criteria},
	}, minConfidence)
	if err != nil {
		return nil, err
	}

	answer, ok := output["answers"].(map[string]any)["score"].(answerOut)
	if !ok {
		return nil, errors.New("the API returned no score answer")
	}
	return map[string]any{
		"model":          output["model"],
		"score":          answer.Score,
		"legend":         answer.Legend,
		"probabilities":  answer.Probabilities,
		"confidence":     answer.Confidence,
		"reliable":       answer.Reliable,
		"min_confidence": minConfidence,
		"latency_ms":     output["latency_ms"],
		"usage":          output["usage"],
	}, nil
}

func toolNoul(raw json.RawMessage) (any, error) {
	var args noulArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("bad arguments: %w", err)
	}
	state, err := checkState(args.State)
	if err != nil {
		return nil, err
	}
	if err := checkInstructions(args.Instructions); err != nil {
		return nil, err
	}
	criteria, err := checkCriteria("noul", args.Criteria)
	if err != nil {
		return nil, err
	}

	threshold := floatOr(args.Threshold, 0.5)
	output, err := runQuestions(state, map[string]apiQuestion{
		"noul": {Type: "noul", Instructions: args.Instructions, Criteria: criteria},
	}, defaultMinConfidence)
	if err != nil {
		return nil, err
	}

	answer, ok := output["answers"].(map[string]any)["noul"].(answerOut)
	if !ok || answer.Noul == nil {
		return nil, errors.New("the API returned no noul answer")
	}
	return map[string]any{
		"model":      output["model"],
		"noul":       *answer.Noul,
		"yes":        *answer.Noul >= threshold,
		"threshold":  threshold,
		"latency_ms": output["latency_ms"],
		"usage":      output["usage"],
	}, nil
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringProperty(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func anyProperty(description string) map[string]any {
	return map[string]any{"description": description}
}

var tools = []toolDef{
	{
		Name: "ask",
		Description: "Evaluate several typed questions against one state in a single request. This is the cheapest and preferred path: batch independent questions instead of calling once per decision, because state is ingested once and questions run in parallel. " +
			"Each question needs id, type (noul, choice, or score) and instructions. criteria is required for choice (an object of option to description) and score (an ordered array of 2 to 10 levels), and optional for noul (an object describing what true and false mean). " +
			"Returns one typed answer per id with probabilities, and confidence plus a reliable flag for choice and score. Noul returns only a 0 to 1 probability. Each call is billed on input tokens only; ask only questions whose answer your code or next step will actually branch on.",
		InputSchema: objectSchema(map[string]any{
			"state": anyProperty("The text or JSON to evaluate: a string, or an object or array for structured state. Reference nested fields from instructions using backticked paths such as `ticket.subject`. Keep it narrow: unrelated detail lowers accuracy."),
			"questions": map[string]any{
				"type":        "array",
				"minItems":    1,
				"description": "Questions to evaluate together against the same state.",
				"items": objectSchema(map[string]any{
					"id":           stringProperty("Your key for this question. The answer comes back under the same id."),
					"type":         map[string]any{"type": "string", "enum": []string{"noul", "choice", "score"}, "description": "noul: yes/no probability. choice: pick one option. score: rate on ordered levels."},
					"instructions": anyProperty("One narrow, literal judgment. State the exact condition, not the intent. A string, or an object or array when the question needs its own data."),
					"criteria":     anyProperty("For choice: an object of {option: description}. For noul: {true: ..., false: ...}. For score use the levels field instead."),
					"levels":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "For score: 2 to 10 ordered level descriptions, weakest to strongest."},
					"options":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "For choice when descriptions are unnecessary: an array of option names."},
				}, "id", "type", "instructions"),
			},
			"min_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Threshold for the reliable flag on choice and score answers. Defaults to 0.5. Informational: a false flag means escalate or ask, not that the call failed."},
		}, "state", "questions"),
		Handler: toolAsk,
	},
	{
		Name:        "choose",
		Description: "Pick one option from a set. Returns choice, per-option probabilities, and confidence. Use for routing, selecting a file or handler, or any closed-set decision. Options may be an object of name to description (better when the distinction needs explaining) or a plain array of names.",
		InputSchema: objectSchema(map[string]any{
			"state":          anyProperty("The text or JSON to decide on: a string, or an object or array for structured state. Keep it narrow."),
			"instructions":   anyProperty("What to decide. Name the exact condition in the wording. A string, or an object or array when the question needs its own data."),
			"options":        map[string]any{"description": "An object of option to description, or an array of option names."},
			"min_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Threshold for the reliable flag. Defaults to 0.5."},
		}, "state", "instructions", "options"),
		Handler: toolChoose,
	},
	{
		Name:        "score",
		Description: "Rate the state on an ordered rubric. Returns a probability-weighted score, the legend, probabilities, and confidence. Use for ranking candidates by a quality or a severity. Do not interpolate between levels to recover a number; threshold the score and keep the arithmetic in code.",
		InputSchema: objectSchema(map[string]any{
			"state":          anyProperty("The text or JSON to rate: a string, or an object or array for structured state. Keep it narrow."),
			"instructions":   anyProperty("The single dimension to rate. One dimension per question; split independent factors and combine them in code."),
			"levels":         map[string]any{"type": "array", "minItems": 2, "maxItems": 10, "items": map[string]any{"type": "string"}, "description": "2 to 10 ordered level descriptions that can each stand alone."},
			"min_confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Threshold for the reliable flag. Defaults to 0.5."},
		}, "state", "instructions", "levels"),
		Handler: toolScore,
	},
	{
		Name:        "noul",
		Description: "Ask a yes/no question and get the probability the answer is yes, from 0 to 1. Use one noul per independent label when several can be true at once; a choice is relative between options, a noul is absolute. There is no confidence value: a noul near 0.5 is a coin flip, not medium intensity. To count matches, ask one noul per item and tally in code.",
		InputSchema: objectSchema(map[string]any{
			"state":        anyProperty("The text or JSON to evaluate: a string, or an object or array for structured state. Keep it narrow."),
			"instructions": anyProperty("The yes/no question. Word it so that yes and no map to the obvious answers."),
			"criteria":     map[string]any{"type": "object", "description": "Optional object describing what true and false mean, for example {true: \"explicitly time-sensitive\", false: \"no urgency expressed\"}."},
			"threshold":    map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Value at or above which yes is reported. Defaults to 0.5."},
		}, "state", "instructions"),
		Handler: toolNoul,
	},
}

func handle(request *rpcRequest) rpcResponse {
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "initialize":
		response.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "jev-mcp", "version": serverVersion},
		}
	case "ping":
		response.Result = map[string]any{}
	case "tools/list":
		list := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
			list = append(list, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema})
		}
		response.Result = map[string]any{"tools": list}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil {
			response.Error = &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
			return response
		}
		for _, tool := range tools {
			if tool.Name != params.Name {
				continue
			}
			result, err := tool.Handler(params.Arguments)
			if err != nil {
				response.Result = toolResult{Content: []toolContent{{Type: "text", Text: err.Error()}}, IsError: true}
				return response
			}
			body, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				body = []byte(fmt.Sprintf("%v", result))
			}
			response.Result = toolResult{Content: []toolContent{{Type: "text", Text: string(body)}}}
			return response
		}
		response.Error = &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}
	default:
		response.Error = &rpcError{Code: -32601, Message: "method not found: " + request.Method}
	}
	return response
}

func run(input io.Reader, output, errorOutput io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1<<20), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var request rpcRequest
		if err := json.Unmarshal(line, &request); err != nil {
			fmt.Fprintf(errorOutput, "jev-mcp: bad input: %v\n", err)
			continue
		}
		if len(request.ID) == 0 {
			continue
		}
		body, err := json.Marshal(handle(&request))
		if err != nil {
			fmt.Fprintf(errorOutput, "jev-mcp: marshal: %v\n", err)
			continue
		}
		if _, err := fmt.Fprintln(output, string(body)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func main() {
	if err := loadConfig(); err != nil {
		fmt.Fprintln(os.Stderr, "jev-mcp:", err)
		os.Exit(1)
	}
	if err := run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "jev-mcp:", err)
		os.Exit(1)
	}
}
