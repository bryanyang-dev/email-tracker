package grouping

import (
	"testing"
	"time"
)

func TestCompareMatchesReplyHeaders(t *testing.T) {
	match := Compare(
		[]Message{{RFCMessageID: "<new@example.com>", InReplyTo: "<old@example.com>"}},
		[]Message{{RFCMessageID: "<old@example.com>"}},
	)
	if !match.Matched || match.Origin != "headers" || match.Confidence != 1 {
		t.Fatalf("Compare() = %#v", match)
	}
}

func TestCompareMatchesRecruitingTopicAndEntityAcrossSubjects(t *testing.T) {
	now := time.Now()
	match := Compare(
		[]Message{{NormalizedSubject: "your interview with snorkel ai", From: "recruiting@snorkel.ai", InternalAt: now}},
		[]Message{{NormalizedSubject: "reminder: snorkel ai technical screen", From: "scheduler@greenhouse.io", InternalAt: now.Add(-24 * time.Hour)}},
	)
	if !match.Matched || match.Reason != "recruiting_topic_and_entity" {
		t.Fatalf("Compare() = %#v", match)
	}
}

func TestCompareDoesNotMergeDifferentInterviewEntities(t *testing.T) {
	now := time.Now()
	match := Compare(
		[]Message{{NormalizedSubject: "interview with snorkel ai", From: "scheduler@greenhouse.io", InternalAt: now}},
		[]Message{{NormalizedSubject: "interview with example corp", From: "scheduler@greenhouse.io", InternalAt: now}},
	)
	if match.Matched {
		t.Fatalf("Compare() unexpectedly matched: %#v", match)
	}
}

func TestCompareUsesSenderNameAsRecruitingEntity(t *testing.T) {
	now := time.Now()
	match := Compare(
		[]Message{{NormalizedSubject: "interview confirmation", From: "Snorkel AI Recruiting <recruiting@snorkel.ai>", InternalAt: now}},
		[]Message{{NormalizedSubject: "technical screen reminder", From: "Snorkel AI <scheduler@greenhouse.io>", InternalAt: now}},
	)
	if !match.Matched || match.Reason != "recruiting_topic_and_entity" {
		t.Fatalf("Compare() = %#v", match)
	}
}

func TestCompareDoesNotUseCandidateNameAsOrganization(t *testing.T) {
	now := time.Now()
	match := Compare(
		[]Message{{NormalizedSubject: "snorkel interview confirmation bryan yang", From: "Recruiter <recruiter@snorkel.ai>", InternalAt: now}},
		[]Message{{NormalizedSubject: "example corp interview bryan yang", From: "Recruiter <recruiter@example.com>", InternalAt: now}},
	)
	if match.Matched {
		t.Fatalf("Compare() unexpectedly matched: %#v", match)
	}
}

func TestCompareIgnoresAccountSenderIdentity(t *testing.T) {
	now := time.Now()
	match := Compare(
		[]Message{
			{NormalizedSubject: "snorkel interview bryan yang", From: "Recruiter <recruiter@snorkel.ai>", InternalAt: now},
			{NormalizedSubject: "snorkel interview bryan yang", From: "Bryan Yang <person@example.com>", InternalAt: now},
		},
		[]Message{
			{NormalizedSubject: "acme interview bryan yang", From: "Recruiter <recruiter@acme.test>", InternalAt: now},
			{NormalizedSubject: "acme interview bryan yang", From: "Bryan Yang <person@example.com>", InternalAt: now},
		},
		"person@example.com",
	)
	if match.Matched {
		t.Fatalf("Compare() unexpectedly matched: %#v", match)
	}
}

func TestCompareMatchesIndexedSnorkelSubjectShapes(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name  string
		left  Message
		right Message
	}{
		{
			name:  "availability and confirmation",
			left:  Message{NormalizedSubject: "snorkel availability request | bryan yang", From: "Tyler Ng <tyler.ng@snorkel.ai>", InternalAt: now},
			right: Message{NormalizedSubject: "snorkel interview confirmation | bryan yang", From: "Tyler Ng <tyler.ng@snorkel.ai>", InternalAt: now},
		},
		{
			name:  "greenhouse prep and direct confirmation",
			left:  Message{NormalizedSubject: "prep for first round with snorkel ai", From: "no-reply@us.greenhouse-mail.io", InternalAt: now},
			right: Message{NormalizedSubject: "snorkel interview confirmation | bryan yang", From: "Tyler Ng <tyler.ng@snorkel.ai>", InternalAt: now},
		},
		{
			name:  "calendar reminder and appointment",
			left:  Message{NormalizedSubject: "reminder: initial chat with joe.rufino@snorkel.ai", From: `"joe.rufino@snorkel.ai (Google Calendar)" <calendar-notification@google.com>`, InternalAt: now},
			right: Message{NormalizedSubject: "appointment booked: initial chat with joe rufino", From: "Joe Rufino <joe.rufino@snorkel.ai>", InternalAt: now},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if match := Compare([]Message{test.left}, []Message{test.right}, "person@example.com"); !match.Matched {
				t.Fatalf("Compare() = %#v", match)
			}
		})
	}
}
