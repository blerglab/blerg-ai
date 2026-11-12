// Same Machado full-severity matrices and ΔE76 floor as core's theme tests.
export const visionMatrices = {
 plain: [1,0,0,0,1,0,0,0,1],
 protanopia: [.152286,1.052583,-.204868,.114503,.786281,.099216,-.003882,-.048116,1.051998],
 deuteranopia: [.367322,.860646,-.227968,.280085,.672501,.047413,-.01182,.04294,.968881],
 tritanopia: [1.255528,-.076749,-.178779,-.078411,.930809,.147602,.004733,.691367,.3039],
}
export function linearRGB(color: string) {
 return color.match(/[\d.]+/g)!.slice(0,3).map(Number).map(n => { const s=n/255; return s<=.04045?s/12.92:((s+.055)/1.055)**2.4 })
}
export function luminance(color: string) { const [r,g,b]=linearRGB(color); return r*.2126+g*.7152+b*.0722 }
export function contrast(a: string,b: string) { const x=luminance(a),y=luminance(b); return (Math.max(x,y)+.05)/(Math.min(x,y)+.05) }
export function simulatedLab(color: string,matrix: number[]) {
 const rgb=linearRGB(color)
 const sim=[0,1,2].map(i=>Math.max(0,Math.min(1,matrix[i*3]*rgb[0]+matrix[i*3+1]*rgb[1]+matrix[i*3+2]*rgb[2])))
 const f=(v:number)=>v>216/24389?Math.cbrt(v):(24389/27*v+16)/116
 const x=f((.4124564*sim[0]+.3575761*sim[1]+.1804375*sim[2])/.95047)
 const y=f(.2126729*sim[0]+.7151522*sim[1]+.072175*sim[2])
 const z=f((.0193339*sim[0]+.119192*sim[1]+.9503041*sim[2])/1.08883)
 return [116*y-16,500*(x-y),200*(y-z)]
}
export function deltaE(a:number[],b:number[]) { return Math.hypot(...a.map((v,i)=>v-b[i])) }
export function renderedBackground(element: Element | null): string {
 if (!element) return 'rgb(255, 255, 255)'
 const raw=getComputedStyle(element).backgroundColor.match(/[\d.]+/g)!.map(Number)
 const alpha=raw[3]??1
 if(alpha===1) return `rgb(${raw.slice(0,3).join(',')})`
 const below=renderedBackground(element.parentElement).match(/[\d.]+/g)!.map(Number)
 return `rgb(${raw.slice(0,3).map((v,i)=>v*alpha+below[i]*(1-alpha)).join(',')})`
}
