import React from 'react';
import { TrendingUp, Globe2, CloudSun, Bitcoin, Wallet } from 'lucide-react';
import GoogleTrendsDashboard from './GoogleTrendsDashboard';
import GdeltDashboard from './gdelt/GdeltDashboard';
import WeatherDashboard from './WeatherDashboard';
import CryptoDashboard from './crypto/CryptoDashboard';
import BillingDashboard from './billing/BillingDashboard';

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
  {
    id: 'gdelt',
    label: 'GDELT News Pulse',
    icon: <Globe2 size={16} />,
    description:
      'Real-time global news sentiment and geopolitical events: tone, conflict hotspots, themes and actors, refreshed every 15 minutes.',
    sourceTable: 'gdelt-bq.gdeltv2',
    component: GdeltDashboard,
  },
  {
    id: 'weather',
    label: 'Global Weather',
    icon: <CloudSun size={16} />,
    description:
      'Daily station observations from NOAA GHCN-Daily: temperatures, precipitation and snow ' +
      'from ~20,000 stations worldwide, about one day behind real time.',
    sourceTable: 'bigquery-public-data.ghcn_d',
    component: WeatherDashboard,
  },
  {
    id: 'crypto',
    label: 'Crypto Pulse',
    icon: <Bitcoin size={16} />,
    description:
      'On-chain fundamentals for Bitcoin and Ethereum: network activity, fee markets, ' +
      'whale flows and the ERC-20 token economy — native units, no fiat prices, updated daily.',
    sourceTable: 'bigquery-public-data.crypto_bitcoin · crypto_ethereum',
    component: CryptoDashboard,
  },
  {
    id: 'gcp_billing',
    label: 'GCP Billing',
    icon: <Wallet size={16} />,
    description:
      'Your own Cloud Billing BigQuery export: cost and usage by service, project, ' +
      'resource and SKU, with credits, discounts and list-price comparisons.',
    sourceTable: 'your billing export dataset (project.dataset)',
    component: BillingDashboard,
  },
];
