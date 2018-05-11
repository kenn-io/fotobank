import click

from fotobank.store import PhotoStore


@click.group()
def cli():
    pass


@cli.command(help="Synchronize metadata with any moved files")
@click.option('-d', '--dry-run', is_flag=True, default=False)
@click.option('-D', '--database')
def sync(**params):
    store = PhotoStore(params['database'])
    store.sync_metadata()


@cli.command(name="import",
             help="Import new files from target directory")
@click.argument("directory_path")
@click.option('-d', '--dry-run', is_flag=True, default=False)
@click.option('-D', '--database')
@click.option('-m', '--move', is_flag=True, default=False)
def import_command(directory_path, **params):
    store = PhotoStore(params['database'])
    store.import_directory(directory_path)


if __name__ == '__main__':
    cli()
