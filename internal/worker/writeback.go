package worker
import("bytes";"context";"crypto/sha256";"encoding/hex";"encoding/json";"errors";"io";"net/http";"net/url";"os";"strings";"time";"github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes")
func(w *Worker) configureCheckin()error{
 base:=strings.TrimRight(os.Getenv("CHECKIN_WORKER_URL"),"/");if base==""{return nil};u,err:=url.Parse(base)
 if err!=nil||u.Host==""||u.User!=nil||u.RawQuery!=""||u.Fragment!=""||u.Path!=""||(u.Scheme!="http"&&u.Scheme!="https"){return errors.New("fixed CHECKIN_WORKER_URL required")}
 secret,err:=readSecret("CHECKIN_SERVICE_SECRET");if err!=nil||len(secret)!=64{return errors.New("CHECKIN_SERVICE_SECRET_FILE required")};w.CheckinURL=base;w.CheckinSecret=secret;return nil
}
func echoFields(s source,e tasknotes.Envelope)([]string,bool){
 if s.Snapshot==nil||s.State!="linked"||(e.Event!="task.updated"&&e.Event!="task.completed"){return nil,false}
 base:=s.EventAt;if base==nil{base=s.Version};if base!=nil&&e.Version().Before(*base)||e.Version().After(time.Now().Add(5*time.Minute)){return nil,false}
 t:=e.EffectiveTask();copy:=t;copy.Status=s.Snapshot.Status;copy.Details=s.Snapshot.Details
 if tasknotes.CanonicalHash(copy,false)!=tasknotes.CanonicalHash(*s.Snapshot,false){return nil,false}
 fields:=[]string{};if t.Status!=s.Snapshot.Status{fields=append(fields,"status")};a,_:=json.Marshal(t.Details);b,_:=json.Marshal(s.Snapshot.Details);if !bytes.Equal(a,b){fields=append(fields,"details")};return fields,true
}
func(w *Worker) checkinEcho(ctx context.Context,c connection,s source,e tasknotes.Envelope)(bool,error){
 if w.CheckinURL==""{return false,nil};fields,candidate:=echoFields(s,e);if !candidate{return false,nil}
 t:=e.EffectiveTask();details:="";if t.Details!=nil{details=strings.TrimSpace(*t.Details)};sum:=sha256.Sum256([]byte(details))
 raw,_:=json.Marshal(map[string]any{"connection_id":c.ID,"source_ref":s.Ref,"path":t.Path,"status":t.Status,"changed_fields":fields,"details_sha256":hex.EncodeToString(sum[:])})
 if err:=w.control(ctx);err!=nil{return false,err};req,err:=http.NewRequestWithContext(ctx,"POST",w.CheckinURL+"/internal/v1/writeback-match",bytes.NewReader(raw));if err!=nil{return false,err};req.Header.Set("Content-Type","application/json");req.Header.Set("Authorization","Bearer "+w.CheckinSecret)
 reply,err:=w.HTTP.Do(req);if err!=nil{return false,err};defer reply.Body.Close();if reply.StatusCode!=200{return false,errors.New("check-in receipt service unavailable")}
 var result struct{Matched bool `json:"matched"`};if json.NewDecoder(io.LimitReader(reply.Body,65536)).Decode(&result)!=nil{return false,errors.New("invalid receipt response")};return result.Matched,nil
}
