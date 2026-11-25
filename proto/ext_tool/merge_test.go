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
	renumbered := Message{
		TypeName:  "agent.v1.EditResult",
		ShortName: "EditResult",
		Package:   "agent.v1",
		Fields: []Field{
			{No: 2, Name: "feedback", Kind: "scalar", T: "string"},
			{No: 1, Name: "path", Kind: "scalar", T: "string"},
		},
	}
	retyped := Message{
		TypeName:  "agent.v1.EditResult",
		ShortName: "EditResult",
		Package:   "agent.v1",
		Fields: []Field{
			{No: 1, Name: "feedback", Kind: "scalar", T: "bool"},
			{No: 2, Name: "path", Kind: "scalar", T: "string"},
		},
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

	// Cùng một thế hệ client: bản copy giữa các bundle lệch metadata ở cùng
	// field là drift descriptor, không phải mâu thuẫn protocol. Chính sách:
	// báo cáo mềm, giữ bản đầu (first-wins).
	crossBundle := mergeBundles([]bundleDefs{
		{ordinal: 0, messages: []Message{extra}},
		{ordinal: 1, messages: []Message{retyped}},
	})
	if len(crossBundle.hardConflicts()) != 0 {
		t.Fatalf("drift metadata cross-bundle không được là hard conflict: %+v", crossBundle.conflicts)
	}
	if got := crossBundle.messages[0].Fields[0]; got.T != "string" {
		t.Fatalf("first-wins phải giữ feedback string, nhận %v", got)
	}

	// Đổi số của cùng tên field là tín hiệu hai biến thể trùng tên, không phải
	// schema bất đồng thật; phải xử lý mềm để -strict không fail trên client 1 thế hệ.
	renumberPool := mergeBundles([]bundleDefs{
		{ordinal: 0, messages: []Message{extra}},
		{ordinal: 1, messages: []Message{renumbered}},
	})
	if len(renumberPool.hardConflicts()) != 0 {
		t.Fatalf("đổi số field trùng tên không được là hard conflict: %+v", renumberPool.conflicts)
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

// TestMergeBundlesSkipsForeignVariantDefinition: hai type KHÁC NHAU trùng tên
// trong cùng package (EditResult bản MCP và bản agent cùng là aiserver.v1)
// phải được xử lý ở mức định nghĩa: bỏ cả định nghĩa sau, ghi xung đột mềm.
// Nếu không, -strict fail hàng nghìn "conflict" không có thật và field-level
// union sinh message lai không tồn tại trong client nào.
func TestMergeBundlesSkipsForeignVariantDefinition(t *testing.T) {
	mcpVariant := Message{
		TypeName: "aiserver.v1.EditResult",
		Fields: []Field{
			{No: 1, Name: "success", Kind: "scalar", T: "bool"},
			{No: 2, Name: "error", Kind: "scalar", T: "string"},
		},
	}
	agentVariant := Message{
		TypeName: "aiserver.v1.EditResult",
		Fields: []Field{
			{No: 1, Name: "path", Kind: "scalar", T: "string"},
			{No: 2, Name: "old_str", Kind: "scalar", T: "string"},
			{No: 3, Name: "new_str", Kind: "scalar", T: "string"},
		},
	}

	pool := mergeBundles([]bundleDefs{
		{ordinal: 0, messages: []Message{mcpVariant}},
		{ordinal: 1, messages: []Message{agentVariant}},
	})
	if len(pool.messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(pool.messages))
	}
	if len(pool.messages[0].Fields) != 2 {
		t.Fatalf("biến thể sau phải bị bỏ cả định nghĩa, fields = %v", pool.messages[0].Fields)
	}
	if len(pool.hardConflicts()) != 0 {
		t.Fatalf("variant khác tên trùng type không được là hard conflict: %+v", pool.conflicts)
	}
	if len(pool.conflicts) == 0 {
		t.Fatal("phải ghi lại xung đột mềm để báo cáo còn nhìn thấy")
	}
}
