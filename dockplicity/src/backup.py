import docker
import random
import json
import glob
import yaml
import socket
import os
import requests
from requests.auth import HTTPBasicAuth
client = docker.DockerClient(base_url='unix://var/run/docker.sock')

def main():
    options = get_options()
    platform_type = get_platform_type(options)
    services = get_services(platform_type, options)
    backup_services(platform_type, services, options)

def get_options():
    return {
        'rancher_url': os.environ['RANCHER_URL'] if 'RANCHER_URL' in os.environ else False,
        'rancher_access_key': os.environ['RANCHER_ACCESS_KEY'] if 'RANCHER_ACCESS_KEY' in os.environ else False,
        'rancher_secret_key': os.environ['RANCHER_SECRET_KEY'] if 'RANCHER_SECRET_KEY' in os.environ else False,
        'volume_driver': os.environ['VOLUME_DRIVER'] if 'VOLUME_DRIVER' in os.environ else 'local',
        'passphrase': os.environ['PASSPHRASE'] if 'PASSPHRASE' in os.environ else 'hellodocker',
        'backup_type': os.environ['BACKUP_TYPE'] if 'BACKUP_TYPE' in os.environ else 'incr',
        'full_if_older_than': os.environ['FULL_IF_OLDER_THAN'] if 'FULL_IF_OLDER_THAN' in os.environ else '2W',
        'target_url': os.environ['TARGET_URL'] if 'TARGET_URL' in os.environ else 'gs://my_google_bucket',
        'remove_older_than': os.environ['REMOVE_OLDER_THAN'] if 'REMOVE_OLDER_THAN' in os.environ else '6M',
        'remove_all_but_n_full': os.environ['REMOVE_ALL_BUT_N_FULL'] if 'REMOVE_ALL_BUT_N_FULL' in os.environ else '12',
        'blacklist': False if os.environ['BLACKLIST'] != 'true' else True,
        'service': False if os.environ['SERVICE'] == '' else os.environ['SERVICE'],
        'data_types': get_data_types(),
        'remove_all_inc_but_of_n_full': os.environ['REMOVE_ALL_INC_BUT_OF_N_FULL'] if 'REMOVE_ALL_INC_BUT_OF_N_FULL' in os.environ else '144'
    }

def get_platform_type(options):
    if options['rancher_url']:
        return 'rancher'
    else:
        return 'docker'

def get_services(platform_type, options):
    services = list()
    if platform_type == 'rancher':
        if options['service']:
            print('service')
        else:
            for service in rancher_call(options, '/services')['data']:
                if len(service['instanceIds']) > 0:
                    container = rancher_call(options, '/containers/' + random.choice(service['instanceIds']))
                    data_type = 'raw'
                    for key, item in options['data_types'].iteritems():
                        image = container['data']['dockerContainer']['Image']
                        if ':' in image:
                            image = image[:image.index(':')]
                        if image == key:
                            data_type = key
                    valid = False
                    if options['blacklist']:
                        valid = True
                labels = container['data']['dockerContainer']['Labels']
                for label in labels.iteritems():
                    if options['blacklist']:
                        if label[0] == 'dockplicity' and label[1] == 'false':
                            valid = False
                    else:
                        if label[0] == 'dockplicity' and label[1] == 'true':
                            valid = True
                if valid:
                    services.append({
                        'name': service['name'],
                        'host': container['hostId'],
                        'data_type': data_type,
                        'volumes': get_volumes(platform_type, options, container)
                    })
    else:
        if options['service']:
            container = client.containers.get(options['service'])
            data_type = 'raw'
            for key, item in options['data_types'].iteritems():
                image = container.attrs['Config']['Image']
                if ':' in image:
                    image = image[:image.index(':')]
                if image == key:
                    data_type = key
            services.append({
                'name': container.attrs['Name'],
                'host': False,
                'data_type': data_type,
                'volumes': get_volumes(platform_type, options, container)
            })
        else:
            for container in client.containers.list():
                data_type = 'raw'
                for key, item in options['data_types'].iteritems():
                    image = container.attrs['Config']['Image']
                    if ':' in image:
                        image = image[:image.index(':')]
                    if image == key:
                        data_type = key
                valid = False
                if options['blacklist']:
                    valid = True
                labels = container.attrs['Config']['Labels']
                for label in labels.iteritems():
                    if options['blacklist']:
                        if label[0] == 'dockplicity' and label[1] == 'false':
                            valid = False
                    else:
                        if label[0] == 'dockplicity' and label[1] == 'true':
                            valid = True
                if valid:
                    services.append({
                        'name': container.attrs['Name'],
                        'host': False,
                        'data_type': data_type,
                        'volumes': get_volumes(platform_type, options, container)
                    })
    return services

def get_volumes(platform_type, options, container):
    volumes = {}
    mounts = {}
    if platform_type == 'rancher':
        mounts = container['data']['dockerContainer']['Mounts']
    else:
        mounts = container.attrs['Mounts']
    for mount in mounts:
        source = mount['Source']
        volume_driver = mount['Driver'] if 'Driver' in mount else 'local'
        if len(source) >= 15 and source[:15] == '/var/lib/docker':
            continue
        if len(source) >= 15 and source[:15] == '/var/run/docker':
            continue
        if len(source) >= 16 and source[:16] == '/var/run/rancher':
            continue
        if volume_driver != options['volume_driver']:
            continue
        destination = ('/volumes/' + mount['Destination'] + '/raw').replace('//', '/')
        volumes[mount['Source']] = {
            'bind': destination,
            'mode': 'rw'
        }
    return volumes

def backup_services(platform_type, services, options):
    environment = {
        'PASSPHRASE': options['passphrase'],
        'BACKUP_TYPE': options['backup_type'],
        'REMOVE_ALL_BUT_N_FULL': options['remove_all_but_n_full'],
        'REMOVE_ALL_INC_BUT_OF_N_FULL': options['remove_all_inc_but_of_n_full'],
        'REMOVE_OLDER_THAN': options['remove_older_than'],
        'FULL_IF_OLDER_THAN': options['full_if_older_than']
    }
    if 'GS_ACCESS_KEY_ID' in os.environ:
        environment['GS_ACCESS_KEY_ID'] = os.environ['GS_ACCESS_KEY_ID']
    if 'GS_SECRET_ACCESS_KEY' in os.environ:
        environment['GS_SECRET_ACCESS_KEY'] = os.environ['GS_SECRET_ACCESS_KEY']
    if platform_type == 'rancher':
        os.system('''
        (echo ''' + options['rancher_url'] + '''; \
        echo ''' + options['rancher_access_key'] + '''; \
        echo ''' + options['rancher_secret_key'] + ''') | rancher config
        ''')
    for service in services:
        if len(service['volumes']) > 0:
            environment['TARGET_URL'] = (options['target_url'] + '/' + service['name']).replace('//', '/')
            environment['CONTAINER_ID'] = service['name']
            environment['DATA_TYPE'] = service['data_type']
            service['volumes']['/var/run/docker.sock'] = {
                'bind': '/var/run/docker.sock',
                'mode': 'rw'
            }
            if platform_type == 'rancher':
                command = 'rancher --host ' + service['host'] + ' docker run --rm --privileged --volume-driver=' + options['volume_driver']
                for key, env in environment.iteritems():
                    command += ' -e ' + key + '=' + env
                for key, vol in service['volumes'].iteritems():
                    command += ' -v ' + key + ':' + vol['bind']
                print(command)
            else:
                response = client.containers.run(
                    image='jamrizzi/dockplicity-backup:latest',
                    volume_driver=options['volume_driver'],
                    volumes=service['volumes'],
                    remove=True,
                    privileged=True,
                    environment=environment
                )
                print(response)
        else:
            print('No volumes to backup for ' + service['name'])

def get_data_types():
    files = ""
    for file in glob.glob("/docker/config/*.yml"):
        files += open(file, 'r').read()
    settings = yaml.load(files)
    return settings

def rancher_call(options, call):
    r = requests.get(options['rancher_url'] + '/v2-beta/' + call, auth=HTTPBasicAuth(options['rancher_access_key'], options['rancher_secret_key']), headers={
        'Content-Type': 'application/json',
        'Accept': 'application/json'
    })
    return r.json()

main()
