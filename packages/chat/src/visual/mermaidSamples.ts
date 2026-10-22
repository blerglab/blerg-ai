// Diagrams shaped like the ones people really publish (board card #84): labels that contain
// words like "image", color styling with classDef, class and style, and a service graph of about
// forty nodes. Used by the policy tests here and the browser tests in the runner app.

export const IMAGE_WORD_LABEL = `flowchart LR
  ctl[vm-image-controller] --> reg[(image registry)]
  ctl --> api[Kubernetes API]`

export const CLASSDEF_AND_CLASS = `flowchart TD
  A[Ingress] --> B[Gateway]
  B --> C[Orders]
  classDef edge fill:#e0f2fe,stroke:#0369a1,stroke-width:2px,color:#0c4a6e
  classDef core fill:#fef3c7,stroke:#b45309,stroke-dasharray: 5 5,font-weight:bold
  class A edge
  class B,C core`

/** About forty services in subgraphs, five classDefs, class lists, ::: shorthand, style,
 *  linkStyle, comments, <br/> line breaks and a title. */
export function serviceGraph(): string {
  const lines = [
    '---',
    'title: Platform service graph',
    '---',
    'flowchart LR',
    '  %% Edge and identity',
    '  subgraph edge[Edge]',
    '    lb[Load balancer] --> ingress[Ingress controller]',
    '    ingress --> waf[WAF<br/>rules]',
    '  end',
    '  subgraph identity[Identity]',
    '    auth[auth-service] --> users[(users db)]',
    '    auth --> tokens[token-issuer]',
    '    tokens --> keys[key-rotation-job]',
    '  end',
  ]
  const services = ['orders', 'billing', 'catalog', 'search', 'inventory', 'shipping', 'notifications', 'payments',
    'reviews', 'recommendations', 'pricing', 'cart', 'checkout', 'fraud-check', 'reporting', 'audit-log']
  lines.push('  subgraph apps[Applications]')
  services.forEach(name => lines.push(`    ${name.replace(/-/g, '_')}[${name}] --> ${name.replace(/-/g, '_')}_db[(${name} db)]`))
  lines.push('  end')
  lines.push('  subgraph platform[Platform]',
    '    vmimg[vm-image-controller] --> registry[(image registry)]',
    '    builder[image-builder] --> registry',
    '    queue{{event bus}} --> workers[worker pool]',
    '    cache[(redis cache)]',
    '  end')
  lines.push('  waf --> auth')
  services.forEach(name => lines.push(`  waf --> ${name.replace(/-/g, '_')}`))
  lines.push('  orders --> queue', '  billing --> queue', 'checkout --> payments', '  payments --> fraud_check', '  search --> cache', '  catalog --> cache')
  lines.push(
    '  classDef edgeTier fill:#e0f2fe,stroke:#0369a1,stroke-width:2px,color:#0c4a6e',
    '  classDef identityTier fill:#ede9fe,stroke:#6d28d9,color:#2e1065',
    '  classDef appTier fill:#ecfccb,stroke:#4d7c0f',
    '  classDef dataTier fill:#fef3c7,stroke:#b45309,stroke-dasharray: 4 2',
    '  classDef platformTier fill:#fee2e2,stroke:#b91c1c,font-style:italic,opacity:0.9',
    '  class lb,ingress,waf edgeTier',
    '  class auth,tokens,keys identityTier',
    `  class ${services.map(n => n.replace(/-/g, '_')).join(',')} appTier`,
    `  class users,cache,registry,${services.map(n => n.replace(/-/g, '_') + '_db').join(',')} dataTier`,
    '  class vmimg,builder,queue,workers platformTier',
    '  style auth stroke-width:3px',
    '  linkStyle 0 stroke:#0369a1,stroke-width:2px',
  )
  return lines.join('\n')
}

/** Parses until line 3, where an arrow points at nothing. */
export const BROKEN = `flowchart LR
  A[Start] --> B[Next]
  B --> --> C
  C --> D`

/** Tall: sixty steps top to bottom. */
export function tallDiagram(): string {
  const lines = ['flowchart TD']
  for (let i = 0; i < 60; i++) lines.push(`  s${i}[Step ${i}] --> s${i + 1}[Step ${i + 1}]`)
  return lines.join('\n')
}
