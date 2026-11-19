import type {Dashboard,DashboardCard} from './dashboard'

export interface VisualTemplate {
 id:string
 question:string
 schema:Record<string,string>
 units:Record<string,string>
 keys:string[]
 domain:string
 freshness:string
 empty:string
 summary:string
 renderer:'native'|'native-static'
 card:DashboardCard
}
const chart=(id:string,title:string,rows:Record<string,string|number|boolean|null>[],spec:Record<string,unknown>):DashboardCard=>({id,kind:'chart',title,span:2,data:{kind:'inline',rows},spec})
const xy=(x:string,y:string,xType='quantitative')=>({x:{field:x,type:xType},y:{field:y,type:'quantitative'},tooltip:[{field:x,type:xType},{field:y,type:'quantitative'}]})
const examples: Array<Omit<VisualTemplate,'freshness'|'empty'|'summary'>>=[
 {id:'time-series',question:'How did latency change?',schema:{id:'string',second:'number',latency:'number'},units:{second:'s since observation start (UTC)',latency:'ms'},keys:['id'],domain:'Observed time extent; numeric values include zero.',renderer:'native',card:chart('time-series','Latency (ms) over time since observation start (s)',[{id:'a',second:0,latency:20},{id:'b',second:30,latency:35},{id:'c',second:60,latency:25}],{mark:'line',encoding:xy('second','latency')})},
 {id:'comparison',question:'Which team completed more?',schema:{team:'string',completed:'number'},units:{completed:'cards'},keys:['team'],domain:'Counts start at zero; categories supplied in descending value order.',renderer:'native',card:chart('comparison','Completed cards by team',[{team:'Cedar',completed:8},{team:'Maple',completed:5},{team:'Oak',completed:2}],{mark:'bar',encoding:{...xy('team','completed','nominal'),x:{field:'team',type:'nominal',sort:null}}})},
 {id:'breakdown',question:'Where was time spent?',schema:{activity:'string',minutes:'number'},units:{minutes:'min'},keys:['activity'],domain:'Zero baseline; descending precomputed totals.',renderer:'native',card:chart('breakdown','Time spent by activity (min)',[{activity:'Build',minutes:40},{activity:'Test',minutes:30},{activity:'Review',minutes:20}],{mark:'bar',encoding:{...xy('activity','minutes','nominal'),x:{field:'activity',type:'nominal',sort:null}}})},
 {id:'distribution',question:'How variable were gate durations?',schema:{gate:'string',seconds:'number'},units:{seconds:'s'},keys:['gate','seconds'],domain:'Boxplot of supplied samples; unknown samples omitted upstream.',renderer:'native-static',card:chart('distribution','Gate duration distribution (s)',[1,2,3,4,8,2,3,4,7,10].map((seconds,i)=>({gate:i<5?'Unit':'Browser',seconds})),{mark:'boxplot',encoding:xy('gate','seconds','nominal')})},
 {id:'stat-sparkline',question:'What is the latest throughput and recent trend?',schema:{value:'number',delta:'number',trend:'number'},units:{value:'cards / h',trend:'cards / h',delta:'cards / h'},keys:[],domain:'Latest supplied row; no interpolation for missing observations.',renderer:'native-static',card:{id:'stat-sparkline',kind:'stat',title:'Throughput',span:1,data:{kind:'inline',rows:[{value:5,delta:1,trend:3},{value:6,delta:1,trend:5}]},view:{value:'value',selection:'latest',unit:'cards / h',delta:'delta',sparkline:'trend'}}},
 {id:'gauge',question:'How much capacity is in use?',schema:{value:'number'},units:{value:'%'},keys:[],domain:'Fixed 0–100%; threshold at 80%.',renderer:'native',card:{id:'gauge',kind:'gauge',title:'Capacity used',span:1,data:{kind:'inline',rows:[{value:62}]},view:{value:'value',selection:'single',unit:'%',min:0,max:100,thresholds:[80]}}},
 {id:'heatmap',question:'When were merges most frequent?',schema:{day:'string',hour:'string',merges:'number | null'},units:{hour:'hour (UTC)',merges:'merges / h'},keys:['day','hour'],domain:'Explicit day/hour order; observed sequential values; null means unknown, zero is measured.',renderer:'native',card:chart('heatmap','Merges per hour (UTC)',[{day:'Mon',hour:'09',merges:2},{day:'Tue',hour:'09',merges:0},{day:'Mon',hour:'10',merges:6},{day:'Tue',hour:'10',merges:null}],{mark:'rect',encoding:{x:{field:'day',type:'ordinal',sort:['Mon','Tue']},y:{field:'hour',type:'ordinal',sort:['09','10']},color:{field:'merges',type:'quantitative'},tooltip:[{field:'day',type:'nominal'},{field:'hour',type:'nominal'},{field:'merges',type:'quantitative'}]}})},
 {id:'status-timeline',question:'When did worker status change?',schema:{id:'string',worker:'string',start:'ISO timestamp with offset',end:'ISO timestamp with offset',status:'string',open:'boolean'},units:{start:'UTC',end:'UTC'},keys:['id'],domain:'Observed interval extent; gaps are unknown, never joined. For open intervals, end is the source observation time and open=true.',renderer:'native',card:chart('status-timeline','Worker status (UTC)',[{id:'a',worker:'Cedar',start:'2026-11-04T09:00:00Z',end:'2026-11-04T10:00:00Z',status:'Working',open:false},{id:'b',worker:'Cedar',start:'2026-11-04T11:00:00Z',end:'2026-11-04T12:00:00Z',status:'Waiting',open:true}],{mark:'rect',encoding:{x:{field:'start',type:'temporal'},x2:{field:'end',type:'temporal'},y:{field:'worker',type:'ordinal'},color:{field:'status',type:'nominal'},tooltip:[{field:'worker',type:'nominal'},{field:'status',type:'nominal'},{field:'start',type:'temporal'},{field:'end',type:'temporal'},{field:'open',type:'nominal'}]}})},
 {id:'histogram',question:'How are gate durations distributed?',schema:{lower:'number',upper:'number',count:'nonnegative number | null'},units:{lower:'s',upper:'s',count:'gates'},keys:['lower','upper'],domain:'Precomputed nonoverlapping bins; [lower, upper), all upper bounds exclusive. Count baseline is zero; missing counts are unknown, not zero.',renderer:'native',card:chart('histogram','Gate durations (s), count of gates',[{lower:0,upper:10,count:3},{lower:10,upper:20,count:7},{lower:20,upper:30,count:2}],{mark:'bar',encoding:{x:{field:'lower',type:'quantitative',bin:'binned'},x2:{field:'upper',type:'quantitative'},y:{field:'count',type:'quantitative'},tooltip:[{field:'lower',type:'quantitative'},{field:'upper',type:'quantitative'},{field:'count',type:'quantitative'}]}})},
 {id:'text',question:'What observations does this view cover?',schema:{text:'string'},units:{},keys:[],domain:'Latest source explanation; no numerical substitution.',renderer:'native',card:{id:'text',kind:'text',title:'Observation coverage',span:4,data:{kind:'inline',rows:[{text:'Simulated observations. Missing intervals represent unknown coverage.'}]},view:{value:'text',selection:'single'}}},
]
/** Readable authoring examples: titles describe the question, not storage field names. */
const axisTitles:Record<string,readonly [string,string]>={
 'time-series':['Elapsed time (s)','Latency (ms)'],
 comparison:['Team','Completed cards'],
 breakdown:['Activity','Time spent (min)'],
 distribution:['Gate','Gate duration (s)'],
 heatmap:['Day','Hour (UTC)'],
 'status-timeline':['Time (UTC)','Worker'],
 histogram:['Gate duration (s)','Number of gates'],
}
/** Small, token-only authoring forms. Metadata stays outside the strict dashboard wire format. */
export const VISUAL_TEMPLATES:readonly VisualTemplate[]=examples.map(template=>{
 if(template.card.kind==='chart'){
  const spec=template.card.spec as {encoding:Record<string,{axis?:{title:string}}>}
  const [x,y]=axisTitles[template.id]
  spec.encoding.x.axis={title:x}
  spec.encoding.y.axis={title:y}
 }
 return {...template,freshness:'Static inline snapshot; live publication uses the file revision observation time.',empty:'No data; missing values are never substituted with zero.',summary:template.card.title}
})
export function templateDashboard():Dashboard {
 return {format:'blerg-dashboard',version:1,title:'Chart catalog · simulated snapshots',cards:VISUAL_TEMPLATES.map(template=>structuredClone(template.card))}
}
