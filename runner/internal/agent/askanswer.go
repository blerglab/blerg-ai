package agent

// AskAnswer is a committed question result delivered by the server's answer
// channel. Conversational input never carries this authority.
type AskAnswer struct {
	QuestionID string
	Text       string // the raw answer; engines match it against their options
	Label      string // who answered, for a free-text reply to a child's model
	AnsweredBy string
	ClientID   string
	Outcome    string
}

// AnswerAsk accepts only typed results from the host's private server poll. It
// never falls back to conversational input, even for stale or duplicate results.
func (l *Loop) AnswerAsk(answer AskAnswer) bool {
	if answer.QuestionID == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if answer.QuestionID == l.interruptedAsk {
		l.interruptedAsk = ""
		l.queueLateAnswerLocked(answer)
		return true
	}
	if l.askCh == nil {
		return false
	}
	if l.askID == "" {
		// The poll may finish before SendAsk returns its id. Match it once the
		// post returns, rather than mistaking a previous question for this one.
		if l.earlyAnswers == nil {
			l.earlyAnswers = make(map[string]AskAnswer)
		}
		l.earlyAnswers[answer.QuestionID] = answer
		return true
	}
	if answer.QuestionID != l.askID {
		return false
	}
	select {
	case l.askCh <- answer:
		return true
	default:
		return false
	}
}

// endInterruptedAsk stops waiting. An answer the server committed as the ask was
// interrupted is not lost: it reaches the model as a message once the turn ends.
func (l *Loop) endInterruptedAsk(id string, ch chan AskAnswer) {
	l.mu.Lock()
	l.askCh, l.askID, l.earlyAnswers = nil, "", nil
	l.interruptedAsk = id
	select {
	case answer := <-ch:
		l.interruptedAsk = ""
		l.queueLateAnswerLocked(answer)
	default:
	}
	l.mu.Unlock()
}

func (l *Loop) queueLateAnswerLocked(answer AskAnswer) {
	if answer.Outcome != "" && answer.Outcome != "answered" {
		return
	}
	l.queue = append(l.queue, Input{Text: answer.Label + "Answer to your question " + answer.QuestionID + ": " + answer.Text, Source: "question_answer"})
	select {
	case l.wake <- struct{}{}:
	default:
	}
}
