// Integration kinds -> the slots they fill. New kinds need a core change, by design.
import type { ComponentType } from 'react'
import type { Integration } from '@/lib/ui-config'
import { uiConfig } from '@/lib/ui-config'
import { OAuthPlusItem, OAuthSettingsRow } from './oauth'
import { ExtensionHeaderButton } from './extension'

type Slot = ComponentType<{ integration: never }>

const kinds: Record<Integration['kind'], { PlusMenuItem?: Slot; HeaderButton?: Slot; SettingsRow?: Slot }> = {
  oauthToolServer: { PlusMenuItem: OAuthPlusItem as Slot, SettingsRow: OAuthSettingsRow as Slot },
  extensionInstall: { HeaderButton: ExtensionHeaderButton as Slot },
}

export function IntegrationSlots({ slot, filter }: { slot: 'PlusMenuItem' | 'HeaderButton' | 'SettingsRow'; filter?: (i: Integration) => boolean }) {
  return (
    <>
      {uiConfig()
        .integrations.filter((i) => !filter || filter(i))
        .map((i) => {
          const C = kinds[i.kind]?.[slot] as ComponentType<{ integration: Integration }> | undefined
          return C ? <C key={i.id} integration={i} /> : null
        })}
    </>
  )
}

export const hasSlot = (slot: 'PlusMenuItem' | 'HeaderButton' | 'SettingsRow', filter?: (i: Integration) => boolean) =>
  uiConfig().integrations.some((i) => kinds[i.kind]?.[slot] && (!filter || filter(i)))
