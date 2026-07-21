import React from 'react';
import { TrendingUp } from 'lucide-react';
import GoogleTrendsDashboard from './GoogleTrendsDashboard';

// Registry of BigQuery Open Data dashboards. To add a new public dataset:
// build its component in this directory, add an entry here, and expose its
// backend endpoints under /api/opendata/<id>/*.
export interface OpenDataset {
  id: string;
  label: string;
  icon: React.ReactNode;
  description: string;
  sourceTable: string;
  component: React.ComponentType;
}

export const OPEN_DATASETS: OpenDataset[] = [
  {
    id: 'google_trends',
    label: 'Google Trends',
    icon: <TrendingUp size={16} />,
    description:
      'Top 25 search terms and top rising queries by country, with 5 years of weekly history per daily snapshot.',
    sourceTable: 'bigquery-public-data.google_trends',
    component: GoogleTrendsDashboard,
  },
];
