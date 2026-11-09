import type { ActivityReport } from '../lib/insightsActivity'
import { skillCohorts, skillNames, skillBenefit, ruleNames, type SkillBenefit } from '../lib/skillInsights'
import { formatDuration } from '../lib/insights'
import './SkillInsights.css'

// BLG-464: a card per skill leads with its verdict (one glyph plus a short word, tone from theme
// tokens) so "is it helping?" answers at a glance; then three numbers (followed, adherence,
// corrections); then the same evidence as before, now in labeled figures instead of run-on prose,
// behind the existing "Evidence by version" disclosure (Density: compact row, click for detail).

const BENEFIT: Record<SkillBenefit['state'], { glyph: string; tone: string; word: string }> = {
 fewer: { glyph: '●', tone: 'tone-ok', word: 'Helping' },
 more: { glyph: '!', tone: 'tone-warn', word: 'Worse' },
 same: { glyph: '●', tone: 'tone-same', word: 'Same' },
 'insufficient-reviews': { glyph: '○', tone: 'tone-unknown', word: 'Not enough evidence yet' },
 'insufficient-versions': { glyph: '○', tone: 'tone-unknown', word: 'Not enough evidence yet' },
}

function Verdict({ benefit }: { benefit: SkillBenefit }) {
 const { glyph, tone, word } = BENEFIT[benefit.state]
 return <span className={`skill-verdict ${tone}`} role="img" aria-label={`Helping: ${benefit.text}`} title={benefit.text}><span aria-hidden="true">{glyph}</span> {word}</span>
}

function Fig({ k, v, title }: { k: string; v: React.ReactNode; title?: string }) {
 return <div title={title}><dt>{k}</dt><dd>{v}</dd></div>
}

export default function SkillInsights({activity}:{activity:ActivityReport}){
 if(activity.skills?.unavailable)return <p className="insights-note">Skill evidence is temporarily unavailable. Other Insights remain available.</p>
 const cohorts=skillCohorts(activity)
 const names=[...new Set(cohorts.map(c=>c.skill))]
 if(!names.length)return <p className="insights-note">No skill loads have been reported in this range. Older conversations and hidden engine reads cannot establish whether a skill was used.</p>
 return <>
  {activity.skills?.truncated&&<p className="insights-note">Some skill records are outside this report’s limit. Narrow the time range.</p>}
  <div className="skill-cards">
   {names.map(name=>{
    const versions=cohorts.filter(c=>c.skill===name)
    const loads=versions.flatMap(v=>v.loads)
    const assessed=versions.flatMap(v=>v.assessed)
    const sessions=new Set(loads.map(l=>l.session_id)).size
    const followed=assessed.filter(e=>e.result==='followed').length
    const missed=assessed.filter(e=>e.result==='missed').length
    // Adherence is the latest version's only: applicability signals are matched by skill, not
    // version (skillCohorts), so summing across versions would double-count a session whose
    // applicability applies to more than one version's cohort.
    const latest=versions[versions.length-1]
    const benefit=skillBenefit(versions)
    return <div className="card skill-card" key={name} data-testid={`skill-card-${name}`}>
     <div className="skill-card-head">
      <Verdict benefit={benefit}/>
      <h2>{skillNames[name]??name}</h2>
     </div>
     <div className="card-hint">{loads.length} {loads.length===1?'load':'loads'} · {sessions} {sessions===1?'conversation':'conversations'} · {versions.length} {versions.length===1?'version':'versions'}</div>
     <dl className="stack-figs three skill-stats" data-testid={`skill-stats-${name}`}>
      <Fig k="Followed" v={assessed.length?`${followed}/${assessed.length}`:'—'}/>
      <Fig k="Adherence" title="Latest version: loaded before relevant edits" v={latest.applicable?`${Math.round(latest.loadedApplicable/latest.applicable*100)}%`:'—'}/>
      <Fig k="Corrections" v={String(missed)}/>
     </dl>
     <details>
      <summary>Evidence by version</summary>
      {versions.map((g,i)=><section key={g.version}>
       <h3 title={g.version}>Version {i+1}</h3>
       {g.reporting.some(r=>r.basis==='content_mismatch')&&<p className="insights-note">A requested skill did not match Blerg’s content. It is excluded from confirmed loads.</p>}
       <dl className="stack-figs">
        <Fig k="On-demand loads" v={String(g.loads.filter(l=>l.mode==='on_demand').length)}/>
        <Fig k="Compact loads" v={String(g.loads.filter(l=>l.mode==='always').length)}/>
        <Fig k="Bytes observed" v={g.bytes.toLocaleString()}/>
        <Fig k="Before relevant edits" v={g.applicable?`${g.loadedApplicable} of ${g.applicable} conversations`:'No relevant edits observed'}/>
        {name==='ux-design'&&<Fig k="Published images" v={String(g.images)}/>}
        {name==='test-driven-development'&&<><Fig k="Full-suite registrations" v={String(g.fullSuites)}/><Fig k="Failures before edit" v={String(g.redBeforeEdit)}/></>}
        <Fig k="Findings" v={`${g.findings} across ${g.reviews} review${g.reviews===1?'':'s'}`}/>
        <Fig k="Rework" v={formatDuration(g.rework)}/>
        <Fig k="Corrections" v={String(g.corrections)}/>
        <Fig k="Reporting conversations" v={String(new Set(g.reporting.map(r=>r.session_id)).size)}/>
        <Fig k="Agent declarations" v={String(g.declared.length)}/>
       </dl>
       {g.mixedCards>0&&<p className="insights-note">{g.mixedCards} card{g.mixedCards===1?'':'s'} with mixed versions excluded.</p>}
       <p className="insights-note">Hidden and truncated reads remain unknown. Bytes, not tokens.</p>
       {g.evidence.length>0&&<ul className="plain skill-evidence">{g.evidence.map((e,j)=><li key={j}>{ruleNames[e.rule]??'Rule assessment'}: {e.result} <span className="muted">({e.source==='person'?'your assessment':'agent declaration'})</span></li>)}</ul>}
      </section>)}
     </details>
    </div>
   })}
  </div>
  <p className="insights-note">Skill changes need your decision. Main may propose a change with this evidence.</p>
  <details className="activity-method"><summary>How this is measured</summary>
   <p className="insights-note">A load shows that instructions entered context. Following a rule and improving results require separate evidence. Comparisons describe the work recorded here; they do not establish cause.</p>
  </details>
 </>
}
