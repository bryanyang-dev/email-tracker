package conversations

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"local-email-workspace/internal/credentials"
	"local-email-workspace/internal/googleapi"
	"local-email-workspace/internal/observability"
	"local-email-workspace/internal/storage"
	"local-email-workspace/internal/triage"
)

type gmailSource interface {
	Thread(context.Context, credentials.OAuthCredential, string) (googleapi.Conversation, error)
}

type triageEvaluator interface {
	Evaluate(context.Context, string, []triage.Message) (triage.Assessment, error)
}

type ModelSelector func(context.Context) (string, error)

type Service struct {
	repository storage.Repository
	gmail      gmailSource
	triage     triageEvaluator
}

type ProcessResult struct {
	Processed        bool
	Created          bool
	ProviderThreadID string
	Visibility       triage.Visibility
	Remaining        int
}

func New(repository storage.Repository, gmail gmailSource, evaluator triageEvaluator) *Service {
	return &Service{repository: repository, gmail: gmail, triage: evaluator}
}

func (s *Service) PendingCount(ctx context.Context, accountID string) (int, error) {
	return s.repository.PendingProviderConversationCount(ctx, accountID)
}

// ProcessNext classifies one provider conversation. Rule-resolved mail avoids a
// body fetch. Everything else is hydrated once, then all later classification
// reads the normalized body from SQLite.
func (s *Service) ProcessNext(
	ctx context.Context,
	account storage.Account,
	credential credentials.OAuthCredential,
	selectModel ModelSelector,
) (ProcessResult, error) {
	pending, err := s.repository.PendingProviderConversations(ctx, account.ID, 1)
	if err != nil {
		return ProcessResult{}, err
	}
	if len(pending) == 0 {
		return ProcessResult{}, nil
	}
	conversation := pending[0]
	if len(conversation.Messages) == 0 {
		return ProcessResult{}, fmt.Errorf("provider conversation %s has no indexed messages", conversation.ProviderThreadID)
	}
	messages := triageMessages(conversation.Messages, true)
	assessment := applyExistingConversationFloor(
		triage.Baseline(messages, account.EmailAddress), conversation,
	)

	if assessment.NeedsAI && needsHydration(conversation.Messages) {
		providerConversation, err := s.gmail.Thread(ctx, credential, conversation.ProviderThreadID)
		if err != nil {
			return ProcessResult{}, fmt.Errorf("hydrate Gmail conversation %s: %w", conversation.ProviderThreadID, err)
		}
		conversation.Messages, err = hydrateKnownMessages(conversation.Messages, providerConversation)
		if err != nil {
			return ProcessResult{}, err
		}
		hydratedMessages := make([]storage.Message, 0, len(conversation.Messages))
		for _, message := range conversation.Messages {
			if message.BodyState != storage.BodyMetadataOnly {
				hydratedMessages = append(hydratedMessages, message)
			}
		}
		if err := s.repository.SaveHydratedMessages(
			ctx, account.ID, conversation.ProviderThreadID, hydratedMessages,
		); err != nil {
			return ProcessResult{}, fmt.Errorf("persist hydrated conversation: %w", err)
		}
		messages = triageMessages(conversation.Messages, true)
		assessment = applyExistingConversationFloor(
			triage.Baseline(messages, account.EmailAddress), conversation,
		)
	}

	aiRequested := assessment.NeedsAI
	if assessment.NeedsAI {
		model, modelErr := selectModel(ctx)
		if modelErr != nil {
			assessment.AIStatus = "unavailable"
			slog.Warn("stored conversation triage failed",
				"request_id", observability.RequestID(ctx),
				"provider_thread_id", conversation.ProviderThreadID,
				"stage", string(triage.StageModelSelection),
				"reason", "model_selection_failed",
			)
		} else {
			modelAssessment, inferenceErr := s.triage.Evaluate(ctx, model, messages)
			if inferenceErr != nil {
				assessment.AIStatus = "failed"
				stage, reason := evaluationFailure(inferenceErr)
				slog.Warn("stored conversation triage failed",
					"request_id", observability.RequestID(ctx),
					"provider_thread_id", conversation.ProviderThreadID,
					"stage", string(stage),
					"reason", string(reason),
				)
			} else {
				assessment = triage.ApplyPolicy(assessment, modelAssessment)
			}
		}
	}

	importance := importanceForVisibility(assessment.Visibility, assessment.Category)
	latest := conversation.Messages[len(conversation.Messages)-1]
	created, err := s.repository.SaveProviderConversationAssessment(ctx, storage.ProviderConversationAssessment{
		AccountID:        account.ID,
		ProviderThreadID: conversation.ProviderThreadID,
		Title:            fallbackTitle(latest.Subject),
		Visibility:       string(assessment.Visibility),
		Category:         assessment.Category,
		Confidence:       assessment.Confidence,
		ReasonCodes:      assessment.ReasonCodes,
		AIStatus:         assessment.AIStatus,
		NeedsAction:      assessment.NeedsAction,
		Urgent:           assessment.Urgent,
		Importance:       importance,
		LastMessageAt:    latest.InternalAt,
	})
	if err != nil {
		return ProcessResult{}, fmt.Errorf("save conversation assessment: %w", err)
	}
	remaining, err := s.repository.PendingProviderConversationCount(ctx, account.ID)
	if err != nil {
		return ProcessResult{}, err
	}
	slog.Info("stored conversation triaged",
		"request_id", observability.RequestID(ctx),
		"provider_thread_id", conversation.ProviderThreadID,
		"message_count", len(messages),
		"visibility", assessment.Visibility,
		"category", assessment.Category,
		"reason_codes", assessment.ReasonCodes,
		"ai_requested", aiRequested,
		"ai_status", assessment.AIStatus,
		"workspace_conversation_created", created,
		"remaining", remaining,
	)
	return ProcessResult{
		Processed:        true,
		Created:          created,
		ProviderThreadID: conversation.ProviderThreadID,
		Visibility:       assessment.Visibility,
		Remaining:        remaining,
	}, nil
}

func needsHydration(messages []storage.Message) bool {
	for _, message := range messages {
		if message.BodyState == storage.BodyMetadataOnly {
			return true
		}
	}
	return false
}

func hydrateKnownMessages(
	stored []storage.Message,
	provider googleapi.Conversation,
) ([]storage.Message, error) {
	providerByID := make(map[string]googleapi.ConversationMessage, len(provider.Messages))
	for _, message := range provider.Messages {
		providerByID[message.ID] = message
	}
	hydrated := make([]storage.Message, len(stored))
	copy(hydrated, stored)
	matched := 0
	for index := range hydrated {
		message, found := providerByID[hydrated[index].ProviderMessageID]
		if !found {
			continue
		}
		matched++
		hydrated[index].Body = message.Body
		hash := sha256.Sum256([]byte(message.Body))
		hydrated[index].BodyHash = fmt.Sprintf("%x", hash[:])
		hydrated[index].BodyState = storage.BodyHydrated
		if message.BodyTruncated {
			hydrated[index].BodyState = storage.BodyTruncated
		}
		hydrated[index].LabelIDs = message.LabelIDs
		hydrated[index].SuspiciousContent = message.SuspiciousContent
		hydrated[index].HasListUnsubscribe = message.HasListUnsubscribe
		hydrated[index].HasListID = message.HasListID
		hydrated[index].Precedence = message.Precedence
		hydrated[index].AutoSubmitted = message.AutoSubmitted
		hydrated[index].HasFeedbackID = message.HasFeedbackID
	}
	if matched == 0 {
		return nil, fmt.Errorf("Gmail conversation %s did not contain any indexed messages", provider.ID)
	}
	return hydrated, nil
}

func triageMessages(messages []storage.Message, useSnippetFallback bool) []triage.Message {
	result := make([]triage.Message, len(messages))
	for index, message := range messages {
		body := message.Body
		if useSnippetFallback && body == "" {
			body = message.Snippet
		}
		result[index] = triage.Message{
			ID:                 message.ProviderMessageID,
			Subject:            message.Subject,
			From:               message.From,
			To:                 message.To,
			Cc:                 message.Cc,
			Date:               message.Date,
			Body:               body,
			LabelIDs:           message.LabelIDs,
			SuspiciousContent:  message.SuspiciousContent,
			HasListUnsubscribe: message.HasListUnsubscribe,
			HasListID:          message.HasListID,
			Precedence:         message.Precedence,
			AutoSubmitted:      message.AutoSubmitted,
			HasFeedbackID:      message.HasFeedbackID,
		}
	}
	return result
}

func importanceForVisibility(visibility triage.Visibility, category string) storage.Importance {
	switch visibility {
	case triage.VisibilityActive:
		return storage.ImportanceImportant
	case triage.VisibilitySuggested:
		return storage.ImportancePossiblyImportant
	case triage.VisibilityAll:
		if category != "other" {
			return storage.ImportanceLowValue
		}
	}
	return storage.ImportanceUnclassified
}

func applyExistingConversationFloor(
	assessment triage.Assessment,
	conversation storage.ProviderConversation,
) triage.Assessment {
	if !conversation.ExistingWorkspaceThread || assessment.Visibility != triage.VisibilityAll || assessment.Category != "other" {
		return assessment
	}
	assessment.Visibility = triage.VisibilitySuggested
	assessment.Category = "important_update"
	assessment.ReasonCodes = []string{"important_update"}
	assessment.NeedsAI = true
	if len(conversation.Messages) > 0 {
		assessment.SourceMessageIDs = []string{conversation.Messages[len(conversation.Messages)-1].ProviderMessageID}
	}
	return assessment
}

func fallbackTitle(subject string) string {
	if strings.TrimSpace(subject) == "" {
		return "(No subject)"
	}
	return subject
}

func evaluationFailure(err error) (triage.FailureStage, observability.FailureReason) {
	var evaluationError *triage.EvaluationError
	if errors.As(err, &evaluationError) {
		return evaluationError.Stage, evaluationError.Reason
	}
	return triage.StageUnknown, triage.ReasonUnknownFailure
}
