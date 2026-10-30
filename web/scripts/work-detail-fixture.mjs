// Synthetic attributed executions; no provider access or paid agent runs.
export function workDetailFixture() {
  const stamp = n => `2026-10-30T00:0${n}:00Z`
  const attempt = (id, role, status, n, session) => ({attempt_id:id,role,operator:role==='reviewer'?'acceptance':'repair-ci',status,session_id:session,machine:'fixture-machine',profile:'fixture-profile',model:'fixture-model',started_at:stamp(n),ended_at:stamp(n+1),elapsed_ms:60000,exit_code:status==='failed'?1:0,error_kind:status==='failed'?'execution_failed':undefined,run_path:`./data/runs/owner__repo/issue-541/${id}/`,provenance:{source:'offload_final',stale:false,observed_at:stamp(n+1)}})
  const attempts = [attempt('failed-first','operator','failed',0,'same-session'),attempt('repair-success','operator','success',2,'same-session'),attempt('review','reviewer','success',4,'independent-review')]
  // Deliberately out of order; the finished legacy record lacks start time and metrics.
  // Keep its end time so unknown activity does not exclude this row from waiting.
  const item = {issue:541,title:'Work detail fixture',state:'open',stage:'ci_pending',why:'Changed PR head requires current-revision CI',evidence:['Repair succeeded; delivery still pending'],updated_at:stamp(6),executions:[attempts[2],{attempt_id:'legacy',status:'unknown',ended_at:stamp(0),provenance:{source:'unknown',stale:false}},attempts[0],attempts[1]],pr:{number:900,url:'https://example.invalid/pull/900',state:'open',head_sha:'bbbbbbb-current-head',updated_at:stamp(6),ci:'pending'},acceptance:{required:true,verdict:'passed',valid:false,attempts:1,infra_attempts:0,completed_at:stamp(5),planner_session:'same-session',reviewer_session:'independent-review'}}
  const history = attempts.map(a=>({...a,path:a.run_path,repo:'owner/repo',issue_number:541,attribution:{role:a.role},availability:{tokens:true,cost:true},usage:{input_tokens:10,output_tokens:2,total_cost_usd:0.01,cost_is_estimate:true,cost_basis:'fixture-estimate'}}))
  const board = {repos:[{repo:'owner/repo',enabled:true,items:[item]}],generated_at:stamp(6),stale:false,refreshing:false,events:[],runtime:{version:'fixture',scheduler:{status:'idle'},execution:{ready:true}}}
  return {item,history,board}
}
