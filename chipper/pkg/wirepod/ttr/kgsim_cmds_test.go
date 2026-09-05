package wirepod_ttr

import (
	"reflect"
	"testing"
)

func TestGetActionsFromString(t *testing.T) {
	say := func(text string) RobotAction {
		return RobotAction{Action: ActionSayText, Parameter: text}
	}
	tests := []struct {
		name  string
		input string
		want  []RobotAction
	}{
		{
			name:  "ordinary command names remain speech",
			input: "  getImage and playAnimationWI are command names. newVoiceRequest is another.  ",
			want:  []RobotAction{say("  getImage and playAnimationWI are command names. newVoiceRequest is another.  ")},
		},
		{
			name:  "speech and all supported actions keep their order",
			input: "Hello {{ playAnimationWI || happy }} there {{playAnimation||sad}}{{getImage||front}} {{newVoiceRequest||now}} Goodbye",
			want: []RobotAction{
				say("Hello"),
				{Action: ActionPlayAnimationWI, Parameter: "happy"},
				say("there"),
				{Action: ActionPlayAnimation, Parameter: "sad"},
				{Action: ActionGetImage, Parameter: "front"},
				{Action: ActionNewRequest, Parameter: "now"},
				say("Goodbye"),
			},
		},
		{
			name:  "separator missing",
			input: "Hello {{playAnimationWI}} world",
			want:  []RobotAction{say("Hello"), say("world")},
		},
		{
			name:  "unknown command discarded",
			input: "{{driveAway||now}} I am still here",
			want:  []RobotAction{say("I am still here")},
		},
		{
			name:  "empty and extra parameters do not execute",
			input: "{{getImage||}}{{getImage||   }}{{||front}}{{getImage||front||now}}{{}}",
		},
		{
			name:  "partial closing tag cannot execute",
			input: "Hello {{getImage||front} world",
			want:  []RobotAction{say("Hello"), say("world")},
		},
		{
			name:  "unfinished tag never reaches speech",
			input: "Hello {{playAnimationWI||happy",
			want:  []RobotAction{say("Hello")},
		},
		{
			name:  "unfinished tag without separator",
			input: "Hello {{playAnimationWI",
			want:  []RobotAction{say("Hello")},
		},
		{
			name:  "nested tag cannot execute",
			input: "{{broken {{getImage||front}}}} Still here",
			want:  []RobotAction{say("Still here")},
		},
		{
			name:  "extra opening or closing braces cannot execute",
			input: "{{{getImage||front}}}{{getImage||front}}} Still here",
			want:  []RobotAction{say("Still here")},
		},
		{
			name:  "valid command following malformed tag still executes",
			input: "{{playAnimationWI}}{{getImage||front}}",
			want:  []RobotAction{{Action: ActionGetImage, Parameter: "front"}},
		},
		{
			name:  "prose following a tag retains names and braces",
			input: "{{playAnimationWI||happy}} getImage is a command; {a, b} is a set. Hello }} world",
			want: []RobotAction{
				{Action: ActionPlayAnimationWI, Parameter: "happy"},
				say("getImage is a command; {a, b} is a set. Hello }} world"),
			},
		},
		{
			name:  "unicode speech survives malformed tags",
			input: "こんにちは {{getImage}} café",
			want:  []RobotAction{say("こんにちは"), say("café")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetActionsFromString(tt.input); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetActionsFromString(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}
