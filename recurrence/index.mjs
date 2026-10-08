import {generateRecurringInstances, getNextUncompletedOccurrence} from '@tasknotes/model/recurrence';
import {buildMaterializeOccurrencePlan, buildMaterializedOccurrenceCompletePlan, buildMaterializedOccurrenceSkipPlan} from '@tasknotes/model/operations';
import rrule from 'rrule';
import {pathToFileURL} from 'node:url';

const datePattern = /^\d{4}-\d{2}-\d{2}$/;
function day(value) {
  if (typeof value !== 'string' || !datePattern.test(value) || new Date(value+'T00:00:00Z').toISOString().slice(0,10)!==value) throw new Error('日期无效。');
  return value;
}
function parent(input) {
  const task = {...input, path: input.path || 'series.md', dateCreated: input.dateCreated || input.scheduled};
  if (typeof task.recurrence !== 'string' || task.recurrence.length>2048 || !task.recurrence.match(/(?:^|;)FREQ=(DAILY|WEEKLY|MONTHLY|YEARLY)(?:;|$)/)) throw new Error('请使用每天、每周、每月或每年的循环规则。');
  // The official model intentionally catches parse errors. Reject invalid rules before it can fall back.
  const rule = task.recurrence.replace(/DTSTART:[^;]+;?/, '').replace(/^;/,'');
  const options = rrule.RRule.parseString(rule);
  if (options.interval !== undefined && (!Number.isInteger(options.interval) || options.interval<1 || options.interval>1000)) throw new Error('循环间隔无效。');
  if (options.count !== undefined && (!Number.isInteger(options.count) || options.count<1 || options.count>100000)) throw new Error('循环次数无效。');
  for (const field of ['byhour','byminute','bysecond']) if (options[field]!==undefined) throw new Error('每期只能对应一个日期，请在开始和结束时间设置时分。');
  if (!task.scheduled || !/^\d{4}-\d{2}-\d{2}(?:T|$)/.test(task.scheduled)) throw new Error('请先设置循环开始日期。');
  day(task.scheduled.slice(0,10));
  if (task.recurrence_anchor && !['scheduled','completion'].includes(task.recurrence_anchor)) throw new Error('循环锚定方式无效。');
  new rrule.RRule(options);
  return task;
}
export function execute(input) {
  const task = parent(input.task);
  const today = day(input.today);
  const now = input.now || today+'T00:00:00Z';
  if (input.mode === 'progress') {
    const date = day(input.date);
    const occurrence = buildMaterializeOccurrencePlan({parentTask: task, targetDate: date, currentTimestamp: now, parentLink: '[[series]]'}).occurrenceTask;
    const args = {parentTask: task, occurrenceTask: occurrence, targetDate: date, currentTimestamp: now, completionDate: input.completion_date || today, completedStatus: input.done_status || 'done', maintainDueDateOffsetInRecurring: true};
    const result = input.skipped ? buildMaterializedOccurrenceSkipPlan(args) : buildMaterializedOccurrenceCompletePlan(args);
    return {version: '0.3.0-rc.9', updates: result.parentUpdates};
  }
  const end = day(input.end);
  const startDate = new Date(today+'T00:00:00Z');
  const endDate = new Date(end+'T23:59:59Z');
  if (endDate<startDate || endDate-startDate>31*86400000) throw new Error('排期范围不能超过三十天。');
  let dates;
  if (task.recurrence_anchor==='completion') {
    // scheduled already represents the next pending period after official completion advancement.
    const scheduledDay = task.scheduled.slice(0,10);
    const processed = new Set([...(task.complete_instances||[]),...(task.skipped_instances||[])]);
    const next = !processed.has(scheduledDay) ? scheduledDay : getNextUncompletedOccurrence(task,{today})?.toISOString().slice(0,10);
    dates = next && next>=today && next<=end ? [next] : [];
  } else {
    dates = generateRecurringInstances(task,startDate,endDate).map(date=>date.toISOString().slice(0,10));
  }
  dates = [...new Set(dates)].sort();
  const processed = new Set([...(task.complete_instances||[]),...(task.skipped_instances||[])]);
  const periods = dates.filter(date=>!processed.has(date)).slice(0,200).map(date=>{
    const plan=buildMaterializeOccurrencePlan({parentTask:task,targetDate:date,currentTimestamp:now,parentLink:input.series_id,allowNonGeneratedTarget:false});
    if (plan.issues.some(issue=>issue.severity==='error')) throw new Error('循环日期与官方规则不一致。');
    return {date,snapshot:plan.occurrenceTask};
  });
  return {version:'0.3.0-rc.9',periods};
}
if (process.argv[1] && import.meta.url===pathToFileURL(process.argv[1]).href) {
  try {
    let raw=''; for await (const chunk of process.stdin) {raw+=chunk;if(raw.length>1024*1024)throw new Error('任务数据过大。');}
    process.stdout.write(JSON.stringify(execute(JSON.parse(raw))));
  } catch(error) {process.stdout.write(JSON.stringify({error:error.message}));process.exitCode=1;}
}
