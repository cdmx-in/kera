// Lazy chunk. ponytail: placeholder until the workspace milestone; routes are reserved.
import { useParams } from 'react-router'
import { Placeholder } from '@/features/admin/AdminPage'

const sections = [
  ['models', 'Models'],
  ['knowledge', 'Knowledge'],
  ['prompts', 'Prompts'],
]

export default function WorkspacePage() {
  const section = useParams()['*']?.split('/')[0] || 'models'
  return <Placeholder title="Workspace" base="/workspace" sections={sections} current={section} />
}
