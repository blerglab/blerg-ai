const day = 86_400_000
export function blackboardAxisTicks(start: number, end: number, width: number): string[] {
 const days = Math.max(1, Math.round((end - start) / day) + 1)
 const count = Math.min(days, Math.max(2, Math.floor((width - 48) / 65)))
 return Array.from({length:count},(_,i)=>new Date(start + Math.round(i*(days-1)/Math.max(1,count-1))*day).toISOString().slice(0,10))
}

export function blackboardCountTicks(maximum: number): number[] {
 const max=Math.max(1,maximum)
 return max <= 1 ? [0,max] : [0,Math.round(max/2),max]
}
