package worker
import (
 "context"
 "encoding/json"
 "github.com/Self-Command/paca-plugin-tasknotes-webhook/internal/tasknotes"
)
// Hydrate every verifiable legacy source before path resolution, including archive moves.
func(w *Worker)hydrateLegacy(ctx context.Context,connectionID string)error{
 rows,err:=w.DB.Query(ctx,"SELECT id,snapshot_hash FROM sources WHERE connection_id=$1 AND snapshot IS NULL AND snapshot_hash<>''",connectionID)
 if err!=nil{return err}
 type oldSource struct{id int64;hash string}
 old:=[]oldSource{}
 for rows.Next(){var s oldSource;if err=rows.Scan(&s.id,&s.hash);err!=nil{rows.Close();return err};old=append(old,s)}
 rows.Close();if rows.Err()!=nil{return rows.Err()}
 for _,s:=range old{
  candidates,queryErr:=w.DB.Query(ctx,"SELECT i.body FROM inbox i WHERE i.connection_id=$1 AND i.state='applied' AND i.body->'data'->'task'->>'path' IN(SELECT path FROM path_aliases WHERE source_id=$2) ORDER BY i.id DESC LIMIT 100",connectionID,s.id)
  if queryErr!=nil{return queryErr}
  var accepted *tasknotes.Envelope
  for candidates.Next(){var raw []byte;if candidates.Scan(&raw)==nil{e,decodeErr:=tasknotes.Decode(raw);if decodeErr==nil && e.LegacySnapshotHash()==s.hash{accepted=&e;break}}}
  candidates.Close();if candidates.Err()!=nil{return candidates.Err()}
  if accepted==nil{continue}
  snapshot,_:=json.Marshal(accepted.EffectiveTask())
  if _,err=w.DB.Exec(ctx,"UPDATE sources SET snapshot=$1::jsonb,event_at=$2,task_modified_at=$3,last_event=$4,snapshot_hash=$5 WHERE id=$6 AND snapshot IS NULL",string(snapshot),accepted.Version(),accepted.TaskModified(),accepted.Event,accepted.SnapshotHash(),s.id);err!=nil{return err}
 }
 return nil
}
