package main

import "testing"

// TestCompactScopesSeparatePackages là hồi quy của Bug B: bundle lớn khai báo
// biến prefix và tên class trùng nhau ở nhiều webpack module, nên tra cứu toàn
// cục khiến message của agent.v1 bị ghi nhầm sang package aiserver.v1.
func TestCompactScopesSeparatePackages(t *testing.T) {
	const bundle = `
1:(e,t,n)=>{
  const a="agent.v1.";
  class B extends n.proto3.Message{static $p(){return a}}
  class MsgA extends B{static $(){return["ConversationTurnStructure|1 x 9"]}}
},
2:(e,t,n)=>{
  const a="aiserver.v1.";
  class B extends n.proto3.Message{static $p(){return a}}
  class MsgB extends B{static $(){return["PluginWorkspaceInfo|1 y 9"]}}
}`

	messages, _, _ := extractCompactProtos(bundle)
	if len(messages) != 2 {
		t.Fatalf("parsed %d messages, want 2", len(messages))
	}
	want := map[string]string{
		"ConversationTurnStructure": "agent.v1",
		"PluginWorkspaceInfo":       "aiserver.v1",
	}
	got := make(map[string]string, len(messages))
	for _, message := range messages {
		got[message.ShortName] = message.Package
	}
	for shortName, pkg := range want {
		if got[shortName] != pkg {
			t.Errorf("%s package = %q, want %q", shortName, got[shortName], pkg)
		}
	}
	if len(got) != len(want) {
		t.Errorf("message set = %v, want %v", got, want)
	}
}

// TestCompactModuleStartsHandlesWebpack5Shorthand bảo vệ detector hình dạng
// module factory thật của bundle 3.32 ("3917(t,e,r){...}"), khác shape mũi tên
// ở test trên.
func TestCompactModuleStartsHandlesWebpack5Shorthand(t *testing.T) {
	const bundle = `{3917(t,e,r){"use strict";const a="agent.v1.";class B extends r.M{static $p(){return a}}class MsgA extends B{static $(){return["ConversationTurnStructure|1 x 9"]}}},12739(t,e,r){"use strict";const a="aiserver.v1.";class B extends r.M{static $p(){return a}}class MsgB extends B{static $(){return["PluginWorkspaceInfo|1 y 9"]}}}}`

	starts := compactModuleStarts(bundle)
	if len(starts) != 2 {
		t.Fatalf("detected %d module boundaries, want 2 (%v)", len(starts), starts)
	}
	messages, _, _ := extractCompactProtos(bundle)
	got := make(map[string]string, len(messages))
	for _, message := range messages {
		got[message.ShortName] = message.Package
	}
	if got["ConversationTurnStructure"] != "agent.v1" {
		t.Errorf("ConversationTurnStructure package = %q, want agent.v1", got["ConversationTurnStructure"])
	}
	if got["PluginWorkspaceInfo"] != "aiserver.v1" {
		t.Errorf("PluginWorkspaceInfo package = %q, want aiserver.v1", got["PluginWorkspaceInfo"])
	}
}

// TestCompactRefResolvesThroughWebpackNamespace: ref "#N" trỏ tới "a.DX" phải
// đi theo bảng import (a = r(3917)) rồi bảng export (DX:()=>CursorRule) của
// module đích, nếu không sẽ lấy nhầm class khác cùng alias name.
func TestCompactRefResolvesThroughWebpackNamespace(t *testing.T) {
	const bundle = `{3917(t,e,r){"use strict";const a="agent.v1.";class Base extends r.M{static $p(){return a}}r.d(e,{DX:()=>CursorRule});class CursorRule extends Base{static $(){return["CursorRule|1 name 9"]}}},12739(t,e,r){"use strict";const a=r(3917);class Base2 extends r.M{static $p(){return a}}class Part extends Base2{static $(){return["RequestContextRulesPart|1 rules #0*",a.DX]}}}}`

	messages, _, _ := extractCompactProtos(bundle)
	var part *Message
	for index := range messages {
		if messages[index].ShortName == "RequestContextRulesPart" {
			part = &messages[index]
		}
	}
	if part == nil {
		t.Fatalf("RequestContextRulesPart not parsed: %+v", messages)
	}
	if len(part.Fields) != 1 {
		t.Fatalf("fields = %d, want 1", len(part.Fields))
	}
	field := part.Fields[0]
	if field.Kind != "message" {
		t.Errorf("field kind = %q, want message", field.Kind)
	}
	if field.T != "agent.v1.CursorRule" {
		t.Errorf("field type = %#v, want agent.v1.CursorRule", field.T)
	}
	if !field.Repeated {
		t.Error("rules must be repeated")
	}
}

// TestCompactNameRegistryCorrectsPackage: khi chain static $p() suy ra package
// khác với bảng tên qualified trong bundle, registry phải thắng.
func TestCompactNameRegistryCorrectsPackage(t *testing.T) {
	const bundle = `{3917(t,e,r){"use strict";const a="aiserver.v1.";class B extends r.M{static $p(){return a}}class MsgA extends B{static $(){return["RequestContextSubagentsPart|1 custom_subagents 9"]}};"agent.v1.RequestContextSubagentsPart":{custom_subagents:"CODE"}}}`

	messages, _, _ := extractCompactProtos(bundle)
	if len(messages) != 1 {
		t.Fatalf("parsed %d messages, want 1", len(messages))
	}
	if messages[0].Package != "agent.v1" {
		t.Fatalf("package = %q, want agent.v1 (registry thắng $p)", messages[0].Package)
	}
	if messages[0].TypeName != "agent.v1.RequestContextSubagentsPart" {
		t.Fatalf("typeName = %q, want agent.v1.RequestContextSubagentsPart", messages[0].TypeName)
	}
}
