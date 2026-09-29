// Geological codenames for auto-naming sessions — Blerg's brand is the
// instrument-panel-on-basalt palette, so sessions get terrain/mineral names
// that live in the same world. Purely cosmetic: a friendly default the user
// can always overwrite at launch.
export const SESSION_NAMES: string[] = [
  // rock + terrain
  'Granite',
  'Basalt',
  'Slate',
  'Flint',
  'Obsidian',
  'Pumice',
  'Gabbro',
  'Gneiss',
  'Schist',
  'Shale',
  'Chert',
  'Marble',
  'Quartz',
  'Onyx',
  'Jasper',
  'Agate',
  'Beryl',
  'Topaz',
  'Zircon',
  'Pyrite',
  'Garnet',
  'Cobalt',
  'Mica',
  'Feldspar',
  // landforms
  'Ridge',
  'Mesa',
  'Butte',
  'Talus',
  'Moraine',
  'Esker',
  'Drumlin',
  'Cirque',
  'Arete',
  'Tarn',
  'Basin',
  'Delta',
  'Rift',
  'Fault',
  'Scarp',
  'Bluff',
  'Canyon',
  'Gorge',
  'Summit',
  'Ridgeline',
  // volcanic
  'Caldera',
  'Cinder',
  'Ember',
  'Magma',
  'Vent',
  'Ashfall',
]

export function randomSessionName(): string {
  return SESSION_NAMES[Math.floor(Math.random() * SESSION_NAMES.length)]
}
