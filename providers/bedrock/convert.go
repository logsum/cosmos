package bedrock

import (
	"cosmos/core/provider"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brdocument "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

const defaultMaxTokens = 4096

func buildConverseStreamInput(req provider.Request) (*bedrockruntime.ConverseStreamInput, error) {
	msgs, err := toBedrockMessages(req.Messages)
	if err != nil {
		return nil, err
	}

	input := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(req.Model),
		Messages: msgs,
	}

	if req.System != "" {
		input.System = []brtypes.SystemContentBlock{
			&brtypes.SystemContentBlockMemberText{Value: req.System},
		}
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}
	input.InferenceConfig = &brtypes.InferenceConfiguration{
		MaxTokens: aws.Int32(int32(maxTokens)),
	}

	if len(req.Tools) > 0 {
		tc, err := toBedrockToolConfig(req.Tools)
		if err != nil {
			return nil, err
		}
		input.ToolConfig = tc
	}

	return input, nil
}

func toBedrockMessages(msgs []provider.Message) ([]brtypes.Message, error) {
	out := make([]brtypes.Message, 0, len(msgs))
	for _, m := range msgs {
		bm, err := toBedrockMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, bm)
	}
	return out, nil
}

func toBedrockMessage(m provider.Message) (brtypes.Message, error) {
	role, err := toBedrockRole(m.Role)
	if err != nil {
		return brtypes.Message{}, err
	}

	msg := brtypes.Message{Role: role}

	if m.Content != "" {
		msg.Content = append(msg.Content, &brtypes.ContentBlockMemberText{Value: m.Content})
	}

	for _, tc := range m.ToolCalls {
		msg.Content = append(msg.Content, &brtypes.ContentBlockMemberToolUse{
			Value: brtypes.ToolUseBlock{
				ToolUseId: aws.String(tc.ID),
				Name:      aws.String(tc.Name),
				Input:     brdocument.NewLazyDocument(tc.Input),
			},
		})
	}

	for _, tr := range m.ToolResults {
		status := brtypes.ToolResultStatusSuccess
		if tr.IsError {
			status = brtypes.ToolResultStatusError
		}
		msg.Content = append(msg.Content, &brtypes.ContentBlockMemberToolResult{
			Value: brtypes.ToolResultBlock{
				ToolUseId: aws.String(tr.ToolUseID),
				Status:    status,
				Content: []brtypes.ToolResultContentBlock{
					&brtypes.ToolResultContentBlockMemberText{Value: tr.Content},
				},
			},
		})
	}

	if len(msg.Content) == 0 {
		return brtypes.Message{}, fmt.Errorf("message with role %q has no content (need text, tool calls, or tool results)", m.Role)
	}

	return msg, nil
}

func toBedrockRole(r provider.Role) (brtypes.ConversationRole, error) {
	switch r {
	case provider.RoleUser:
		return brtypes.ConversationRoleUser, nil
	case provider.RoleAssistant:
		return brtypes.ConversationRoleAssistant, nil
	default:
		return "", fmt.Errorf("unknown message role: %q", r)
	}
}

func toBedrockToolConfig(tools []provider.ToolDefinition) (*brtypes.ToolConfiguration, error) {
	brTools := make([]brtypes.Tool, len(tools))
	for i, t := range tools {
		brTools[i] = &brtypes.ToolMemberToolSpec{
			Value: brtypes.ToolSpecification{
				Name:        aws.String(t.Name),
				Description: aws.String(t.Description),
				InputSchema: &brtypes.ToolInputSchemaMemberJson{
					Value: brdocument.NewLazyDocument(t.InputSchema),
				},
			},
		}
	}

	return &brtypes.ToolConfiguration{Tools: brTools}, nil
}
