package domain

// QuestionIdentity is the catalog and native journal identity shown to a human.
// Execution distinguishes continuations that reuse the same question digest.
type QuestionIdentity struct {
	MissionID    string `json:"mission_id"`
	WorkspaceID  string `json:"workspace_id"`
	Delivery     string `json:"delivery"`
	Task         string `json:"task"`
	Execution    int    `json:"execution"`
	QuestionID   string `json:"question_id"`
	OpenedDigest string `json:"opened_digest"`
}

type PendingQuestion struct {
	Target QuestionIdentity `json:"target"`
	Prompt string           `json:"prompt"`
}

type AnswerRequest struct {
	MissionID   string `json:"mission_id"`
	WorkspaceID string `json:"workspace_id"`
	// Workspace is retained for older consumers, but cannot authorize an answer.
	Workspace    string `json:"workspace,omitempty"`
	Delivery     string `json:"delivery"`
	Task         string `json:"task"`
	Execution    int    `json:"execution"`
	QuestionID   string `json:"question_id"`
	OpenedDigest string `json:"opened_digest"`
	Text         string `json:"text"`
}

func (r AnswerRequest) Identity() QuestionIdentity {
	return QuestionIdentity{MissionID: r.MissionID, WorkspaceID: r.WorkspaceID, Delivery: r.Delivery, Task: r.Task, Execution: r.Execution, QuestionID: r.QuestionID, OpenedDigest: r.OpenedDigest}
}

func (q QuestionIdentity) Answer(text string) AnswerRequest {
	return AnswerRequest{MissionID: q.MissionID, WorkspaceID: q.WorkspaceID, Delivery: q.Delivery, Task: q.Task, Execution: q.Execution, QuestionID: q.QuestionID, OpenedDigest: q.OpenedDigest, Text: text}
}

type AnswerEffect string

const (
	AnswerRejected  AnswerEffect = "rejected"
	AnswerRecorded  AnswerEffect = "recorded"
	AnswerUncertain AnswerEffect = "uncertain"
)

type AnswerFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type AnswerResult struct {
	Target   QuestionIdentity `json:"target"`
	Delivery string           `json:"delivery,omitempty"`
	Effect   AnswerEffect     `json:"effect"`
	Error    *AnswerFailure   `json:"error,omitempty"`
}
