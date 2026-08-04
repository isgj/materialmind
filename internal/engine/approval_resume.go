package engine

import (
	"context"
	"iter"
	"sync/atomic"

	"google.golang.org/adk/v2/model"
)

type approvalYieldContextKey struct{}

type approvalYieldControl struct {
	enabled atomic.Bool
}

func withApprovalYield(ctx context.Context, enabled bool) context.Context {
	control := &approvalYieldControl{}
	control.enabled.Store(enabled)
	return context.WithValue(ctx, approvalYieldContextKey{}, control)
}

func shouldYieldAfterApproval(ctx context.Context) bool {
	control, _ := ctx.Value(approvalYieldContextKey{}).(*approvalYieldControl)
	return control != nil && control.enabled.Load()
}

func enableApprovalYield(ctx context.Context) {
	control, _ := ctx.Value(approvalYieldContextKey{}).(*approvalYieldControl)
	if control != nil {
		control.enabled.Store(true)
	}
}

type approvalYieldModel struct {
	model.LLM
}

func (m *approvalYieldModel) GenerateContent(
	ctx context.Context,
	request *model.LLMRequest,
	stream bool,
) iter.Seq2[*model.LLMResponse, error] {
	if shouldYieldAfterApproval(ctx) {
		return func(func(*model.LLMResponse, error) bool) {}
	}
	return m.LLM.GenerateContent(ctx, request, stream)
}
