package tasksync

import (
	"encoding/json"
	"testing"
)

func TestManagedPhotosNeverBecomeTaskDescription(t *testing.T) {
	value := "Task\n<!-- paca-checkin:start -->\n![[PushGo附件/photo.jpg]]\n<!-- paca-checkin:end -->"
	if TaskBody(value) != "Task" {
		t.Fatal("managed photo entered task body")
	}
}
func TestSharedBodyPreservesPrivateOutside(t *testing.T) {
	value := "private\n<!-- paca-task-content:start -->\nshared\n<!-- paca-task-content:end -->\nlocal"
	if TaskBody(value) != "shared" {
		t.Fatal("private notes entered sync")
	}
}
func TestCommonContentRoundTrip(t *testing.T) {
	body := "# Title\n- First\n- [x] Checked\n> Quote\n**Bold** and *italic*"
	raw, _ := json.Marshal(Blocks(body))
	got, err := Markdown(raw)
	if err != nil || got != body {
		t.Fatalf("content changed: %q %v", got, err)
	}
}
func TestUnknownRichBlockIsNotSilentlyDropped(t *testing.T) {
	_, err := Markdown(json.RawMessage(`[{"type":"unknown-widget","content":[]}]`))
	if err == nil {
		t.Fatal("unsupported original content dropped")
	}
}

func TestLinksTablesNumberedAndNestedListRoundTrip(t *testing.T) {
 body := "## 文档\n1. [说明](https://example.org)\n2. 第二项\n- 父项\n  - 子项\n| 字段 | 内容 |\n| --- | --- |\n| 时间 | 九点 |"
 raw,_:=json.Marshal(Blocks(body));got,err:=Markdown(raw)
 if err!=nil||got!=body {t.Fatalf("rich Markdown changed: %q %v",got,err)}
}
