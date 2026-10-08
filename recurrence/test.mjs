import test from 'node:test';
import assert from 'node:assert/strict';
import {generateRecurringInstances} from '@tasknotes/model/recurrence';
import {buildMaterializeOccurrencePlan,buildMaterializedOccurrenceCompletePlan} from '@tasknotes/model/operations';
import {execute} from './index.mjs';
const base={title:'每日阅读',path:'Tasks/阅读.md',scheduled:'2026-10-08T09:00',due:'2026-10-08T10:00',recurrence:'FREQ=DAILY',recurrence_anchor:'scheduled',status:'open',priority:'high',tags:['学习']};
const args={today:'2026-10-08',end:'2026-11-07',now:'2026-10-08T00:00:00Z',series_id:'stable-series',task:base};
test('fixed calendar and materialization exactly follow official model',()=>{
  for(const recurrence of ['FREQ=DAILY','FREQ=WEEKLY;BYDAY=MO,WE,FR','FREQ=MONTHLY;BYMONTHDAY=31','FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29']){
    const task={...base,recurrence};const result=execute({...args,task});
    const expected=generateRecurringInstances(task,new Date(args.today+'T00:00:00Z'),new Date(args.end+'T00:00:00Z')).map(d=>d.toISOString().slice(0,10));
    assert.deepEqual(result.periods.map(p=>p.date),expected);
    for(const p of result.periods){const official=buildMaterializeOccurrencePlan({parentTask:task,targetDate:p.date,currentTimestamp:args.now,parentLink:args.series_id});assert.deepEqual(p.snapshot,official.occurrenceTask);assert.equal(p.snapshot.recurrence,undefined);}
  }
});
test('leap day, month end and cross-day due preserve official dates',()=>{
  const leap=execute({...args,today:'2028-02-01',end:'2028-03-02',task:{...base,scheduled:'2024-02-29T23:30',due:'2024-03-01T00:30',recurrence:'FREQ=YEARLY;BYMONTH=2;BYMONTHDAY=29'}});
  assert.equal(leap.periods[0].date,'2028-02-29');assert.equal(leap.periods[0].snapshot.due.slice(0,10),'2028-03-01');
  const month=execute({...args,today:'2026-04-01',end:'2026-05-01',task:{...base,scheduled:'2026-01-31T09:00',due:'2026-01-31T10:00',recurrence:'FREQ=MONTHLY;BYMONTHDAY=31'}});assert.deepEqual(month.periods,[]);
});
test('completion anchor keeps exactly one pending occurrence and advancement is official',()=>{
  const task={...base,recurrence_anchor:'completion'};const periods=execute({...args,task}).periods;assert.equal(periods.length,1);assert.equal(periods[0].date,args.today);
  const progress=execute({...args,task,mode:'progress',date:args.today,completion_date:'2026-10-10',done_status:'done'});
  const occurrence=buildMaterializeOccurrencePlan({parentTask:task,targetDate:args.today,currentTimestamp:args.now,parentLink:'[[series]]'}).occurrenceTask;
  const official=buildMaterializedOccurrenceCompletePlan({parentTask:task,occurrenceTask:occurrence,targetDate:args.today,currentTimestamp:args.now,completionDate:'2026-10-10',completedStatus:'done',maintainDueDateOffsetInRecurring:true});
  assert.deepEqual(progress.updates,official.parentUpdates);
  assert.equal(execute({...args,task:{...task,...progress.updates}}).periods.length,1);
});
test('completed/skipped periods, date-only time precision and invalid rules',()=>{
  const result=execute({...args,task:{...base,scheduled:'2026-10-08',due:undefined,complete_instances:['2026-10-08'],skipped_instances:['2026-10-09']}});
  assert.equal(result.periods[0].date,'2026-10-10');assert.equal(result.periods[0].snapshot.scheduled,'2026-10-10');
  for(const rule of ['FREQ=HOURLY','FREQ=DAILY;INTERVAL=0','FREQ=DAILY;BAD=1','FREQ=DAILY;BYHOUR=9,10'])assert.throws(()=>execute({...args,task:{...base,recurrence:rule}}));
});
