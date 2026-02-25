package bedrock

import (
	"cosmos/core/provider"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

type blockKind int

const (
	blockText blockKind = iota
	blockTool
)

// eventStream is the interface satisfied by bedrockruntime's ConverseStreamEventStream.
// Defined as an interface for testability.
type eventStream interface {
	Events() <-chan brtypes.ConverseStreamOutput
	Close() error
	Err() error
}

type bedrockIterator struct {
	stream      eventStream
	events      <-chan brtypes.ConverseStreamOutput
	block       blockKind
	pendingStop *provider.StreamChunk
	done        bool
}

func (it *bedrockIterator) Next() (provider.StreamChunk, error) {
	for {
		if it.done {
			return provider.StreamChunk{}, io.EOF
		}

		event, ok := <-it.events
		if !ok {
			// Channel closed — stream finished.
			it.done = true
			if err := it.stream.Err(); err != nil {
				return provider.StreamChunk{}, fmt.Errorf("bedrock stream: %w", classifyErr(err))
			}
			if it.pendingStop != nil {
				chunk := *it.pendingStop
				it.pendingStop = nil
				return chunk, nil
			}
			return provider.StreamChunk{}, io.EOF
		}

		if chunk, ok := it.translate(event); ok {
			return chunk, nil
		}
	}
}

func (it *bedrockIterator) Close() error {
	it.done = true
	return it.stream.Close()
}

func (it *bedrockIterator) translate(event brtypes.ConverseStreamOutput) (provider.StreamChunk, bool) {
	switch v := event.(type) {
	case *brtypes.ConverseStreamOutputMemberContentBlockStart:
		return it.handleBlockStart(v.Value)

	case *brtypes.ConverseStreamOutputMemberContentBlockDelta:
		return it.handleBlockDelta(v.Value)

	case *brtypes.ConverseStreamOutputMemberContentBlockStop:
		return it.handleBlockStop()

	case *brtypes.ConverseStreamOutputMemberMessageStop:
		it.pendingStop = &provider.StreamChunk{
			Event:      provider.EventMessageStop,
			StopReason: string(v.Value.StopReason),
		}
		return provider.StreamChunk{}, false

	case *brtypes.ConverseStreamOutputMemberMetadata:
		if it.pendingStop != nil && v.Value.Usage != nil {
			it.pendingStop.Usage = &provider.Usage{
				InputTokens:  int(aws.ToInt32(v.Value.Usage.InputTokens)),
				OutputTokens: int(aws.ToInt32(v.Value.Usage.OutputTokens)),
			}
		}
		return provider.StreamChunk{}, false

	default:
		return provider.StreamChunk{}, false
	}
}

func (it *bedrockIterator) handleBlockStart(event brtypes.ContentBlockStartEvent) (provider.StreamChunk, bool) {
	switch start := event.Start.(type) {
	case *brtypes.ContentBlockStartMemberToolUse:
		it.block = blockTool
		return provider.StreamChunk{
			Event:      provider.EventToolStart,
			ToolCallID: aws.ToString(start.Value.ToolUseId),
			ToolName:   aws.ToString(start.Value.Name),
		}, true
	default:
		it.block = blockText
		return provider.StreamChunk{}, false
	}
}

func (it *bedrockIterator) handleBlockDelta(event brtypes.ContentBlockDeltaEvent) (provider.StreamChunk, bool) {
	switch delta := event.Delta.(type) {
	case *brtypes.ContentBlockDeltaMemberText:
		return provider.StreamChunk{
			Event: provider.EventTextDelta,
			Text:  delta.Value,
		}, true
	case *brtypes.ContentBlockDeltaMemberToolUse:
		return provider.StreamChunk{
			Event:      provider.EventToolDelta,
			InputDelta: aws.ToString(delta.Value.Input),
		}, true
	default:
		return provider.StreamChunk{}, false
	}
}

func (it *bedrockIterator) handleBlockStop() (provider.StreamChunk, bool) {
	if it.block == blockTool {
		it.block = blockText
		return provider.StreamChunk{Event: provider.EventToolEnd}, true
	}
	return provider.StreamChunk{}, false
}
