import click
import os

from fotobank.store import PhotoStore


DATABASE_ENV_PATH = os.environ.get('FOTOBANK_DATABASE_PATH')


@click.group()
def cli():
    pass


def _get_database(database):
    if database is not None:
        return database

    if DATABASE_ENV_PATH is None:
        raise ValueError('Must pass database (-D) or set '
                         'FOTOBANK_DATABASE_PATH environment variable')
    else:
        return DATABASE_ENV_PATH


DATABASE_HELP = "Path to database, defaults to $FOTOBANK_DATABASE_PATH"
DRY_RUN_HELP = "Print, but do not perform the intended actions"


@cli.command(name='sync-metadata',
             help="Synchronize metadata with any moved files")
@click.option('-d', '--dry-run', is_flag=True, default=False,
              help=DRY_RUN_HELP)
@click.option('-D', '--database', default=None,
              help=DATABASE_HELP)
def sync_metadata(database, dry_run, **params):
    database = _get_database(database)
    store = PhotoStore(database)
    store.sync_metadata()


@cli.command(name="import",
             help="Import new files from target directory")
@click.argument("directory_path")
@click.option('-d', '--dry-run', is_flag=True, default=False,
              help=DRY_RUN_HELP)
@click.option('-D', '--database', default=None, help=DATABASE_HELP)
@click.option('-m', '--move', is_flag=True, default=False,
              help="Move files instead of copying")
@click.option('-v', '--verbose', is_flag=True, default=False,
              help=("If true, print extra logging, including "
                    "duplicates skipped"))
def import_command(database, directory_path, dry_run, move, verbose,
                   **params):
    database = _get_database(database)
    store = PhotoStore(database, verbose=verbose)
    store.import_directory(directory_path, dry_run=dry_run,
                           move=move)


if __name__ == '__main__':
    cli()
