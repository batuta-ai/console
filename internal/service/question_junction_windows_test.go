package service

import (
	"github.com/batuta-ai/console/internal/testfixture"
	"testing"
)

func TestQuestionMissionIsolationAndWorkspaceReplacementJunction(t *testing.T) {
	checkQuestionMissionIsolationAndWorkspaceReplacement(t, testfixture.DirectoryJunction)
}
