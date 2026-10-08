package tasksync

import (
 "encoding/json"
 "errors"
 "fmt"
 "regexp"
 "strconv"
 "strings"
)

var cards = regexp.MustCompile("(?s)<!-- paca-checkin:start -->.*?<!-- paca-checkin:end -->")
func TaskBody(value string) string {
 value=regexp.MustCompile(`<!-- paca-sync-id:[a-fA-F0-9-]{36} -->`).ReplaceAllString(value,"")
 value=cards.ReplaceAllString(value,"")
 start,end:="<!-- paca-task-content:start -->","<!-- paca-task-content:end -->"
 a,b:=strings.Index(value,start),strings.Index(value,end)
 if a>=0&&b>=a+len(start){return strings.TrimSpace(value[a+len(start):b])}
 return strings.TrimSpace(value)
}
func inlineText(value any)(string,error){
 list,ok:=value.([]any);if !ok{return "",errors.New("unsupported inline content")};var out strings.Builder
 for _,v:=range list{item,ok:=v.(map[string]any);if !ok{return "",errors.New("unsupported inline value")};kind,_:=item["type"].(string)
  if kind=="link" {text,err:=inlineText(item["content"]);if err!=nil{return "",err};href,_:=item["href"].(string);out.WriteString("["+text+"]("+href+")");continue}
  if kind!="text" {return "",errors.New("unsupported inline type")};text,_:=item["text"].(string);styles,_:=item["styles"].(map[string]any)
  for _,style:=range []struct{key,mark string}{{"code",string(rune(96))},{"bold","**"},{"italic","*"},{"strike","~~"}}{if yes,_:=styles[style.key].(bool);yes{text=style.mark+text+style.mark}}
  out.WriteString(text)
 };return out.String(),nil
}
func Markdown(raw json.RawMessage)(string,error){
 if len(raw)==0||string(raw)=="null"{return "",nil}
 var plain string;if json.Unmarshal(raw,&plain)==nil{return TaskBody(plain),nil}
 var blocks []any;if json.Unmarshal(raw,&blocks)!=nil{return "",errors.New("unsupported description")}
 text,err:=blockLines(blocks,0);return TaskBody(text),err
}
func blockLines(blocks []any,depth int)(string,error){
 if depth>12{return "",errors.New("description nesting too deep")};lines:=[]string{}
 for index,value:=range blocks{b,ok:=value.(map[string]any);if !ok{return "",errors.New("unsupported block")};kind,_:=b["type"].(string);props,_:=b["props"].(map[string]any);text:="";var err error
  if kind=="table"{content,ok:=b["content"].(map[string]any);if !ok{return "",errors.New("unsupported table")};rows,ok:=content["rows"].([]any);if !ok{return "",errors.New("unsupported table rows")};table:=[]string{}
   for i,row:=range rows{r,ok:=row.(map[string]any);if !ok{return "",errors.New("unsupported table row")};cells,ok:=r["cells"].([]any);if !ok{return "",errors.New("unsupported table cells")};columns:=[]string{};for _,cell:=range cells{c,err:=inlineText(cell);if err!=nil{return "",err};columns=append(columns,strings.ReplaceAll(c,"|","\\|"))};table=append(table,"| "+strings.Join(columns," | ")+" |");if i==0{separator:=make([]string,len(cells));for j:=range separator{separator[j]="---"};table=append(table,"| "+strings.Join(separator," | ")+" |")}}
   text=strings.Join(table,"\n")
  }else{if b["content"]!=nil{text,err=inlineText(b["content"]);if err!=nil{return "",err}}
   switch kind{case "paragraph":case "heading":level:=intNumber(props["level"]);if level<1||level>6{level=2};text=strings.Repeat("#",level)+" "+text
   case "bulletListItem":text="- "+text
   case "numberedListItem":text=strconv.Itoa(index+1)+". "+text
   case "checkListItem":checked,_:=props["checked"].(bool);mark:=" ";if checked{mark="x"};text="- ["+mark+"] "+text
   case "quote":text="> "+text
   case "codeBlock":lang,_:=props["language"].(string);fence:=strings.Repeat(string(rune(96)),3);text=fence+lang+"\n"+text+"\n"+fence
   default:return "",fmt.Errorf("unsupported block type %s",kind)}
  };lines=append(lines,text)
  if children,ok:=b["children"].([]any);ok&&len(children)>0{nested,err:=blockLines(children,depth+1);if err!=nil{return "",err};lines=append(lines,"  "+strings.ReplaceAll(nested,"\n","\n  "))}
 };return strings.Join(lines,"\n"),nil
}
func intNumber(v any)int{switch n:=v.(type){case float64:return int(n);case int:return n;default:return 0}}
func richInline(text string)[]any{
 out:=[]any{};for len(text)>0{begin:=-1;chosen:="";for _,mark:=range []string{"**","~~",string(rune(96)),"*"}{if p:=strings.Index(text,mark);p>=0&&(begin<0||p<begin){begin=p;chosen=mark}}
  if begin<0 {out=append(out,map[string]any{"type":"text","text":text,"styles":map[string]any{}});break}
  stop:=strings.Index(text[begin+len(chosen):],chosen);if stop<0||stop==0 {out=append(out,map[string]any{"type":"text","text":text,"styles":map[string]any{}});break}
  if begin>0{out=append(out,map[string]any{"type":"text","text":text[:begin],"styles":map[string]any{}})}
  key:=map[string]string{"**":"bold","~~":"strike","*":"italic",string(rune(96)):"code"}[chosen];out=append(out,map[string]any{"type":"text","text":text[begin+len(chosen):begin+len(chosen)+stop],"styles":map[string]any{key:true}});text=text[begin+2*len(chosen)+stop:]
 };return out
}
func Blocks(markdown string)[]any{
 blocks:=[]any{};lines:=strings.Split(TaskBody(markdown),"\n");fence:=strings.Repeat(string(rune(96)),3)
 for i:=0;i<len(lines);i++{line:=lines[i];kind:="paragraph";props:=map[string]any{};content:=line
  if strings.HasPrefix(line,fence){kind="codeBlock";props["language"]=strings.TrimSpace(strings.TrimPrefix(line,fence));code:=[]string{};for i++;i<len(lines)&&!strings.HasPrefix(lines[i],fence);i++{code=append(code,lines[i])};content=strings.Join(code,"\n")
  }else if strings.HasPrefix(line,"#") {n:=len(line)-len(strings.TrimLeft(line,"#"));if n<=6&&len(line)>n&&line[n]==' ' {kind="heading";props["level"]=n;content=line[n+1:]}
  }else if strings.HasPrefix(line,"- [ ] ")||strings.HasPrefix(line,"- [x] ") {kind="checkListItem";props["checked"]=strings.HasPrefix(line,"- [x]");content=line[6:]
  }else if strings.HasPrefix(line,"- ") {kind="bulletListItem";content=line[2:]
  }else if strings.HasPrefix(line,"> ") {kind="quote";content=line[2:]}
  inline:=richInline(content);if kind=="codeBlock"{inline=[]any{map[string]any{"type":"text","text":content,"styles":map[string]any{}}}}
  blocks=append(blocks,map[string]any{"type":kind,"props":props,"content":inline,"children":[]any{}})
 };return blocks
}
