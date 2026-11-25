package main

import "testing"

// TestMergeBundlesUnionsFieldsAndGradesConflicts: merge phải cộng thêm field
// number mới, và chỉ coi là xung đột cứng khi hai BUNDLE khác nhau nói khác
// nhau; trùng tên trong cùng một bundle là xung đột mềm.
func TestMergeBundlesUnionsFieldsAndGradesConflicts(t *testing.T) {
	shared := Message{
		TypeName:  "agent.v1.EditResult",
		ShortName: "EditResult",
		Package:   "agent.v1",
		Fields:    []Field{{No: 1, Name: "feedback", Kind: "scalar", T: "string"}},
	}
	extra := Message{
		TypeName:  "agent.v1.EditResult",
		ShortName: "EditResult",
		Package:   "agent.v1",
		Fields: []Field{
			{No: 1, Name: "feedback", Kind: "scalar", T: "string"},
			{No: 2, Name: "path", Kind: "scalar", T: "string"},
		},
	}
	clash := Message{
		TypeName:  "agent.v1.EditResult",
		ShortName: "EditResult",
		Package:   "agent.v1",
		Fields:    []Field{{No: 1, Name: "success", Kind: "scalar", T: "bool"}},
	}

	sameBundle := mergeBundles([]bundleDefs{
		{ordinal: 0, messages: []Message{shared, clash}},
	})
	if len(sameBundle.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(sameBundle.messages))
	}
	if len(sameBundle.messages[0].Fields) != 1 {
		t.Fatalf("fields = %d, want 1", len(sameBundle.messages[0].Fields))
	}
	if len(sameBundle.hardConflicts()) != 0 {
		t.Fatalf("xung đột trong cùng bundle không được là hard: %+v", sameBundle.conflicts)
	}

	crossBundle := mergeBundles([]bundleDefs{
		{ordinal: 0, messages: []Message{shared}},
		{ordinal: 1, messages: []Message{clash}},
	})
	if len(crossBundle.hardConflicts()) == 0 {
		t.Fatal("hai bundle nói khác nhau về cùng field number phải là hard conflict")
	}

	union := mergeBundles([]bundleDefs{
		{ordinal: 0, messages: []Message{shared}},
		{ordinal: 1, messages: []Message{extra}},
	})
	if len(union.messages) != 1 || len(union.messages[0].Fields) != 2 {
		t.Fatalf("union fields = %v, want 2 phần tử", union.messages[0].Fields)
	}
	if len(union.hardConflicts()) != 0 {
		t.Fatalf("union không được sinh hard conflict: %+v", union.conflicts)
	}
}

// TestMergeBundlesDropsDuplicateFieldName: hai biến thể cùng tên type (như
// CreatePlanResult bản success/error + plan_uri#3 và bản accepted/rejected +
// plan_uri#4) không được phép gộp thành message trùng tên field, vì protoc sẽ
// từ chối. Bản định nghĩa sau phải bị bỏ ở field trùng tên.
func TestMergeBundlesDropsDuplicateFieldName(t *testing.T) {
	first := Message{
		TypeName: "agent.v1.CreatePlanResult",
		Fields: []Field{
			{No: 1, Name: "success", Kind: "message", T: "agent.v1.CreatePlanSuccess"},
			{No: 3, Name: "plan_uri", Kind: "scalar", T: "string"},
		},
	}
	second := Message{
		TypeName: "agent.v1.CreatePlanResult",
		Fields: []Field{
			{No: 1, Name: "accepted", Kind: "message", T: "agent.v1.CreatePlanAccepted"},
			{No: 4, Name: "plan_uri", Kind: "scalar", T: "string"},
		},
	}

	// Cùng bundle và khác bundle đều phải giữ đúng một field plan_uri.
	for _, bundles := range [][]bundleDefs{
		{{ordinal: 0, messages: []Message{first, second}}},
		{{ordinal: 0, messages: []Message{first}}, {ordinal: 1, messages: []Message{second}}},
	} {
		pool := mergeBundles(bundles)
		if len(pool.messages) != 1 {
			t.Fatalf("messages = %d, want 1", len(pool.messages))
		}
		nameCount := make(map[string]int)
		for _, field := range pool.messages[0].Fields {
			nameCount[field.Name]++
		}
		if nameCount["plan_uri"] != 1 {
			t.Fatalf("plan_uri xuất hiện %d lần trong %v", nameCount["plan_uri"], pool.messages[0].Fields)
		}
		if nameCount["success"] != 1 {
			t.Fatalf("biến thể đầu phải được giữ, fields = %v", pool.messages[0].Fields)
		}
	}
}
